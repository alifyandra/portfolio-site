// Package fargate launches a container on ECS Fargate and runs it to completion,
// reporting success by the container's exit code. It is the run-to-completion half
// of the async platform (ADR 0013): the worker consumes a heavy Job, launches a
// task, blocks on DescribeTasks until it STOPs, and acks SQS only on exit 0.
// Distinct from ADR 11's run-and-connect Fargate mode (which dials the task over a
// private IP); this launcher never talks to the task, it only watches it exit.
package fargate

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// maxRunToCompletion caps how long the worker blocks on one task. The SQS queue's
// visibility timeout MUST be set above this (ADR 0013) so a slow run is never
// redelivered and duplicated mid-flight.
const maxRunToCompletion = 15 * time.Minute

// Config identifies the ECS task to launch. Kept free of any appconfig dependency
// so the launcher stays a reusable platform piece (the caller maps its env vars in).
type Config struct {
	Region          string
	Cluster         string
	TaskDefinition  string // family; ECS resolves the latest ACTIVE revision at RunTask
	ContainerName   string // the container the env override targets and whose exit code is read
	Subnets         []string
	SecurityGroupID string
	AssignPublicIP  bool
}

// Launcher runs a task to completion on Fargate.
type Launcher struct {
	ecs *ecs.Client
	cfg Config
}

// NewLauncher builds a launcher and its ECS client. Only constructed when the
// worker is in a Fargate dispatch mode, so local dev needs no AWS credentials.
func NewLauncher(ctx context.Context, cfg Config) (*Launcher, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("fargate: loading aws config: %w", err)
	}
	return &Launcher{ecs: ecs.NewFromConfig(awsCfg), cfg: cfg}, nil
}

// RunToCompletion launches the task (overriding the container's environment with
// env), blocks polling DescribeTasks until it STOPs, and returns nil only when the
// container exited 0. A nil exit code, a non-zero exit, a launch failure, or the
// poll cap are all errors — the worker leaves the SQS message for redelivery.
func (l *Launcher) RunToCompletion(ctx context.Context, env map[string]string) error {
	assign := ecstypes.AssignPublicIpDisabled
	if l.cfg.AssignPublicIP {
		// A public subnet has no NAT, so the ECS agent needs a public IP to pull the
		// image from ECR and read secrets from SSM, or the task never starts.
		assign = ecstypes.AssignPublicIpEnabled
	}

	// Bound the RunTask call so a hung RPC cannot block the worker; if the worker is
	// shutting down, aborting it is fine (the SQS message redelivers).
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	run, err := l.ecs.RunTask(runCtx, &ecs.RunTaskInput{
		Cluster:        aws.String(l.cfg.Cluster),
		TaskDefinition: aws.String(l.cfg.TaskDefinition),
		LaunchType:     ecstypes.LaunchTypeFargate,
		Count:          aws.Int32(1),
		Overrides: &ecstypes.TaskOverride{
			ContainerOverrides: []ecstypes.ContainerOverride{{
				Name:        aws.String(l.cfg.ContainerName),
				Environment: kvPairs(env),
			}},
		},
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        l.cfg.Subnets,
				SecurityGroups: []string{l.cfg.SecurityGroupID},
				AssignPublicIp: assign,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("fargate: run task: %w", err)
	}
	if len(run.Failures) > 0 {
		f := run.Failures[0]
		return fmt.Errorf("fargate: run task failed: %s (%s)", aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	if len(run.Tasks) == 0 || run.Tasks[0].TaskArn == nil {
		return fmt.Errorf("fargate: run task returned no task")
	}

	return l.awaitExit(ctx, aws.ToString(run.Tasks[0].TaskArn))
}

// awaitExit polls DescribeTasks until the task STOPs, then checks the container's
// exit code.
func (l *Launcher) awaitExit(ctx context.Context, taskArn string) error {
	deadline := time.Now().Add(maxRunToCompletion)
	for {
		out, err := l.ecs.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(l.cfg.Cluster),
			Tasks:   []string{taskArn},
		})
		if err != nil {
			// Abandon the task rather than leave it running while SQS redelivers a
			// duplicate: we can no longer observe it, so stop it and fail.
			l.stopTask(taskArn, "digest: describe tasks failed")
			return fmt.Errorf("fargate: describe tasks: %w", err)
		}
		if len(out.Tasks) == 0 {
			return fmt.Errorf("fargate: task %s disappeared", taskArn)
		}
		t := out.Tasks[0]
		if aws.ToString(t.LastStatus) == "STOPPED" {
			return exitError(t, l.cfg.ContainerName)
		}
		if time.Now().After(deadline) {
			// Over the hard cap: stop the task so it does not keep running past the
			// SQS visibility window and get duplicated by the redelivered message.
			l.stopTask(taskArn, "digest: exceeded run-to-completion cap")
			return fmt.Errorf("fargate: task did not stop within %s (last status %q)", maxRunToCompletion, aws.ToString(t.LastStatus))
		}
		select {
		case <-ctx.Done():
			l.stopTask(taskArn, "digest: worker shutting down")
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// stopTask best-effort stops a task the launcher is abandoning (cap exceeded, poll
// error, or worker shutdown) so it does not keep running and get duplicated when SQS
// redelivers the message. Uses a fresh background context so a cancelled parent ctx
// still issues the stop; errors are ignored (nothing more we can do). The
// ecs:StopTask permission is provisioned in Terraform (digest.tf).
func (l *Launcher) stopTask(taskArn, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = l.ecs.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(l.cfg.Cluster),
		Task:    aws.String(taskArn),
		Reason:  aws.String(reason),
	})
}

// exitError inspects a STOPPED task and returns nil only when the named container
// exited 0. A nil exit code (the task died before the container ran — image-pull
// failure, OOM, stopped by the platform) is treated as failure, carrying the
// task's StoppedReason. A pure function so it can be unit-tested without a live
// ECS call.
func exitError(task ecstypes.Task, containerName string) error {
	for _, c := range task.Containers {
		if aws.ToString(c.Name) != containerName {
			continue
		}
		if c.ExitCode == nil {
			return fmt.Errorf("fargate: container %q has no exit code: %s", containerName, aws.ToString(task.StoppedReason))
		}
		if *c.ExitCode != 0 {
			return fmt.Errorf("fargate: container %q exited %d: %s", containerName, *c.ExitCode, aws.ToString(c.Reason))
		}
		return nil
	}
	return fmt.Errorf("fargate: container %q not found in stopped task: %s", containerName, aws.ToString(task.StoppedReason))
}

// kvPairs converts an env map to ECS KeyValuePairs, sorted by key for a stable
// override order.
func kvPairs(env map[string]string) []ecstypes.KeyValuePair {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]ecstypes.KeyValuePair, 0, len(env))
	for _, k := range keys {
		pairs = append(pairs, ecstypes.KeyValuePair{Name: aws.String(k), Value: aws.String(env[k])})
	}
	return pairs
}
