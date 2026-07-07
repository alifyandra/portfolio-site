package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	appconfig "github.com/alifyandra/portfolio-site/backend/internal/config"
)

// SidecarHandle is a ready sidecar endpoint for ONE batch. Client is dialed for
// the life of the batch; Close tears the sidecar down (StopTask for fargate, a
// no-op for static). Close must always be called (defer it), and should be handed
// a context detached from the request so teardown still runs after the browser
// disconnects.
type SidecarHandle struct {
	Client *Client
	stop   func(context.Context)
}

// Close tears down the sidecar for this batch. Safe to call on a nil handle or a
// handle with no teardown (static mode).
func (h *SidecarHandle) Close(ctx context.Context) {
	if h != nil && h.stop != nil {
		h.stop(ctx)
	}
}

// SidecarProvider readies a sidecar for a single batch, hiding whether that is a
// fixed URL (static, local dev) or a freshly launched Fargate task (prod). The
// handler is written against this interface so it is mode-agnostic.
type SidecarProvider interface {
	// Configured reports whether sends are possible at all. False => the create
	// endpoint returns 503 before any batch row is written.
	Configured() bool
	// Provision readies a sidecar for one batch and returns a handle whose Client is
	// ready to StartSession. onProvisioning is invoked with a human message during a
	// cold start (fargate) so the caller can inform the browser; it is never called
	// in static mode. The caller must Close the returned handle.
	Provision(ctx context.Context, onProvisioning func(message string)) (*SidecarHandle, error)
}

// StaticProvider dials a fixed sidecar URL for every batch (local dev). Configured
// mirrors the old Client.Configured: a blank URL disables sends.
type StaticProvider struct {
	url    string
	secret string
}

// NewStaticProvider builds a provider bound to a fixed sidecar URL.
func NewStaticProvider(url, secret string) *StaticProvider {
	return &StaticProvider{url: url, secret: secret}
}

func (p *StaticProvider) Configured() bool { return p.url != "" }

// Provision returns a client for the fixed URL with no teardown. It emits no
// cold-start messages: the static sidecar is assumed already running.
func (p *StaticProvider) Provision(_ context.Context, _ func(string)) (*SidecarHandle, error) {
	if !p.Configured() {
		return nil, errors.New("whatsapp: sidecar not configured")
	}
	return &SidecarHandle{Client: NewClient(p.url, p.secret)}, nil
}

// FargateProvider launches a per-batch ECS Fargate task, resolves its private IP,
// waits for its HTTP server to answer, and dials it. The task is stopped when the
// batch ends (SidecarHandle.Close). It always reports Configured: the required
// identifiers are validated at config load, so if it exists it can run.
type FargateProvider struct {
	ecs    *ecs.Client
	ec2    *ec2.Client
	secret string

	cluster        string
	taskDef        string
	subnets        []string
	securityGroup  string
	assignPublicIP bool
	port           int
}

func (p *FargateProvider) Configured() bool { return true }

// Provision runs a task, waits for RUNNING, resolves its ENI private IP, waits for
// /healthz, and returns a handle that stops the task on Close. Any failure AFTER
// RunTask succeeded stops the launched task before returning, so a failed provision
// never orphans a running (billing) task.
func (p *FargateProvider) Provision(ctx context.Context, onProvisioning func(message string)) (*SidecarHandle, error) {
	if onProvisioning == nil {
		onProvisioning = func(string) {}
	}
	onProvisioning("starting the sender")

	assign := ecstypes.AssignPublicIpDisabled
	if p.assignPublicIP {
		// A public subnet has no NAT, so the ECS agent needs a public IP to pull the
		// image from ECR and read the secret from SSM. Without it the task never
		// leaves PROVISIONING and eventually stops.
		assign = ecstypes.AssignPublicIpEnabled
	}

	// Run the task on a context detached from ctx. If the browser disconnects during
	// the RunTask RPC, we still want the task ARN back so we can StopTask it: AWS may
	// have accepted the request and launched a task, and without the ARN it would
	// bill until self-exit. Bounded so a hung call cannot block. The cold-start
	// polling below still uses ctx and aborts promptly on cancel.
	runCtx, cancelRun := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancelRun()
	run, err := p.ecs.RunTask(runCtx, &ecs.RunTaskInput{
		Cluster:        aws.String(p.cluster),
		TaskDefinition: aws.String(p.taskDef),
		LaunchType:     ecstypes.LaunchTypeFargate,
		Count:          aws.Int32(1),
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        p.subnets,
				SecurityGroups: []string{p.securityGroup},
				AssignPublicIp: assign,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("wa fargate: run task: %w", err)
	}
	if len(run.Failures) > 0 {
		f := run.Failures[0]
		return nil, fmt.Errorf("wa fargate: run task failed: %s (%s)", aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	if len(run.Tasks) == 0 || run.Tasks[0].TaskArn == nil {
		return nil, errors.New("wa fargate: run task returned no task")
	}
	taskArn := aws.ToString(run.Tasks[0].TaskArn)

	// From here on the task is billing, so tear it down on any failure path.
	stop := func(sctx context.Context) {
		_, _ = p.ecs.StopTask(sctx, &ecs.StopTaskInput{
			Cluster: aws.String(p.cluster),
			Task:    aws.String(taskArn),
			Reason:  aws.String("batch relay ended"),
		})
	}
	fail := func(err error) (*SidecarHandle, error) {
		// Best-effort teardown on a short detached context so a canceled ctx (client
		// gone during cold start) still stops the task.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		stop(sctx)
		return nil, err
	}

	// If the client disconnected during RunTask, stop the task we just launched
	// instead of polling it to RUNNING.
	if err := ctx.Err(); err != nil {
		return fail(err)
	}

	task, err := p.waitRunning(ctx, taskArn, onProvisioning)
	if err != nil {
		return fail(err)
	}

	ip, err := p.privateIP(ctx, task)
	if err != nil {
		return fail(err)
	}

	baseURL := fmt.Sprintf("http://%s:%d", ip, p.port)
	if err := p.waitHealthy(ctx, baseURL, onProvisioning); err != nil {
		return fail(err)
	}

	return &SidecarHandle{Client: NewClient(baseURL, p.secret), stop: stop}, nil
}

// waitRunning polls DescribeTasks until the task reaches RUNNING (~90s cap). A
// terminal-ish LastStatus (the task went down before it came up) is a hard error
// carrying the StoppedReason.
func (p *FargateProvider) waitRunning(ctx context.Context, taskArn string, onProvisioning func(string)) (ecstypes.Task, error) {
	const maxWait = 90 * time.Second
	deadline := time.Now().Add(maxWait)
	for {
		out, err := p.ecs.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(p.cluster),
			Tasks:   []string{taskArn},
		})
		if err != nil {
			return ecstypes.Task{}, fmt.Errorf("wa fargate: describe tasks: %w", err)
		}
		if len(out.Tasks) == 0 {
			return ecstypes.Task{}, errors.New("wa fargate: task disappeared before RUNNING")
		}
		t := out.Tasks[0]
		switch aws.ToString(t.LastStatus) {
		case "RUNNING":
			return t, nil
		// Any shutdown state means the task failed to come up.
		case "DEACTIVATING", "STOPPING", "DEPROVISIONING", "STOPPED":
			return ecstypes.Task{}, fmt.Errorf("wa fargate: task stopped before RUNNING: %s", aws.ToString(t.StoppedReason))
		}
		if time.Now().After(deadline) {
			return ecstypes.Task{}, fmt.Errorf("wa fargate: task did not reach RUNNING within %s (last status %q)", maxWait, aws.ToString(t.LastStatus))
		}
		onProvisioning("starting the sender")
		select {
		case <-ctx.Done():
			return ecstypes.Task{}, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// privateIP resolves the task's private IP: read the ENI id from the task's
// ElasticNetworkInterface attachment, then DescribeNetworkInterfaces for its
// PrivateIpAddress.
func (p *FargateProvider) privateIP(ctx context.Context, task ecstypes.Task) (string, error) {
	eniID := eniIDFromTask(task)
	if eniID == "" {
		return "", errors.New("wa fargate: task has no ElasticNetworkInterface attachment")
	}
	out, err := p.ec2.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
		NetworkInterfaceIds: []string{eniID},
	})
	if err != nil {
		return "", fmt.Errorf("wa fargate: describe network interface %s: %w", eniID, err)
	}
	if len(out.NetworkInterfaces) == 0 || out.NetworkInterfaces[0].PrivateIpAddress == nil {
		return "", fmt.Errorf("wa fargate: no private IP for eni %s", eniID)
	}
	return aws.ToString(out.NetworkInterfaces[0].PrivateIpAddress), nil
}

// eniIDFromTask extracts the ENI id from a RUNNING task's ElasticNetworkInterface
// attachment, or "" if absent. Pulled out as a pure function so the extraction can
// be unit-tested without a live ECS call.
func eniIDFromTask(task ecstypes.Task) string {
	for _, att := range task.Attachments {
		if aws.ToString(att.Type) != "ElasticNetworkInterface" {
			continue
		}
		for _, d := range att.Details {
			if aws.ToString(d.Name) == "networkInterfaceId" {
				return aws.ToString(d.Value)
			}
		}
	}
	return ""
}

// waitHealthy polls GET <baseURL>/healthz (no auth) until 200 or ~30s. A RUNNING
// task does not mean the Node server is listening yet, so this closes the gap
// before we hand back a client.
func (p *FargateProvider) waitHealthy(ctx context.Context, baseURL string, onProvisioning func(string)) error {
	const maxWait = 30 * time.Second
	client := &http.Client{Timeout: 5 * time.Second}
	healthURL := baseURL + "/healthz"
	deadline := time.Now().Add(maxWait)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			return fmt.Errorf("wa fargate: build health request: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wa fargate: sidecar at %s did not become healthy within %s", baseURL, maxWait)
		}
		onProvisioning("warming up the sender")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// NewSidecarProvider builds the provider for the configured mode. The AWS clients
// are constructed only in the fargate branch, so static/local dev needs no AWS
// credentials. The mode is validated in config.Load, so an unknown value cannot
// reach here (it falls through to static defensively).
func NewSidecarProvider(ctx context.Context, cfg *appconfig.Config) (SidecarProvider, error) {
	switch cfg.WaSidecarMode {
	case "fargate":
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
		if err != nil {
			return nil, fmt.Errorf("wa fargate: loading aws config: %w", err)
		}
		return &FargateProvider{
			ecs:            ecs.NewFromConfig(awsCfg),
			ec2:            ec2.NewFromConfig(awsCfg),
			secret:         cfg.WaSidecarSecret,
			cluster:        cfg.WaEcsCluster,
			taskDef:        cfg.WaTaskDefinition,
			subnets:        cfg.WaSubnetIDs,
			securityGroup:  cfg.WaSecurityGroupID,
			assignPublicIP: cfg.WaAssignPublicIP,
			port:           cfg.WaSidecarPort,
		}, nil
	default: // "static"
		return NewStaticProvider(cfg.WaSidecarURL, cfg.WaSidecarSecret), nil
	}
}
