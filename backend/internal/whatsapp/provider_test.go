package whatsapp

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

func TestEniIDFromTask(t *testing.T) {
	cases := []struct {
		name string
		task ecstypes.Task
		want string
	}{
		{
			name: "eni id present among details",
			task: ecstypes.Task{Attachments: []ecstypes.Attachment{{
				Type: aws.String("ElasticNetworkInterface"),
				Details: []ecstypes.KeyValuePair{
					{Name: aws.String("subnetId"), Value: aws.String("subnet-a")},
					{Name: aws.String("networkInterfaceId"), Value: aws.String("eni-0abc123")},
					{Name: aws.String("privateIPv4Address"), Value: aws.String("10.0.1.5")},
				},
			}}},
			want: "eni-0abc123",
		},
		{
			name: "wrong attachment type is ignored",
			task: ecstypes.Task{Attachments: []ecstypes.Attachment{{
				Type: aws.String("Service Connect"),
				Details: []ecstypes.KeyValuePair{
					{Name: aws.String("networkInterfaceId"), Value: aws.String("eni-ignored")},
				},
			}}},
			want: "",
		},
		{
			name: "no attachments",
			task: ecstypes.Task{},
			want: "",
		},
		{
			name: "eni attachment without a networkInterfaceId detail",
			task: ecstypes.Task{Attachments: []ecstypes.Attachment{{
				Type: aws.String("ElasticNetworkInterface"),
				Details: []ecstypes.KeyValuePair{
					{Name: aws.String("subnetId"), Value: aws.String("subnet-a")},
				},
			}}},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := eniIDFromTask(c.task); got != c.want {
				t.Errorf("eniIDFromTask() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestStaticProvider(t *testing.T) {
	if (&StaticProvider{}).Configured() {
		t.Error("a blank-URL StaticProvider should report not Configured")
	}

	p := NewStaticProvider("http://localhost:8081", "secret")
	if !p.Configured() {
		t.Fatal("StaticProvider with a URL should be Configured")
	}

	h, err := p.Provision(context.Background(), nil)
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if h.Client == nil || !h.Client.Configured() {
		t.Fatal("Provision() returned a nil/unconfigured client")
	}
	// Static mode has no teardown: Close must be a safe no-op (no panic on nil stop).
	h.Close(context.Background())
}
