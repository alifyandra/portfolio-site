package fargate

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

func TestExitError(t *testing.T) {
	cases := []struct {
		name      string
		task      ecstypes.Task
		container string
		wantNil   bool
		wantMsg   string // substring expected in a non-nil error
	}{
		{
			name: "exit 0 is success",
			task: ecstypes.Task{Containers: []ecstypes.Container{
				{Name: aws.String("digest"), ExitCode: aws.Int32(0)},
			}},
			container: "digest",
			wantNil:   true,
		},
		{
			name: "non-zero exit is an error carrying the code",
			task: ecstypes.Task{Containers: []ecstypes.Container{
				{Name: aws.String("digest"), ExitCode: aws.Int32(1), Reason: aws.String("boom")},
			}},
			container: "digest",
			wantMsg:   "exited 1",
		},
		{
			name: "nil exit code is a failure with the stopped reason",
			task: ecstypes.Task{
				StoppedReason: aws.String("CannotPullContainerError"),
				Containers:    []ecstypes.Container{{Name: aws.String("digest")}},
			},
			container: "digest",
			wantMsg:   "no exit code",
		},
		{
			name:      "container not found",
			task:      ecstypes.Task{StoppedReason: aws.String("gone"), Containers: nil},
			container: "digest",
			wantMsg:   "not found",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := exitError(c.task, c.container)
			if c.wantNil {
				if err != nil {
					t.Fatalf("exitError = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantMsg) {
				t.Fatalf("exitError = %v, want an error containing %q", err, c.wantMsg)
			}
		})
	}
}

func TestKVPairs_SortedAndComplete(t *testing.T) {
	pairs := kvPairs(map[string]string{"B": "2", "A": "1"})
	if len(pairs) != 2 {
		t.Fatalf("len = %d, want 2", len(pairs))
	}
	if aws.ToString(pairs[0].Name) != "A" || aws.ToString(pairs[1].Name) != "B" {
		t.Errorf("not sorted by key: %q, %q", aws.ToString(pairs[0].Name), aws.ToString(pairs[1].Name))
	}
	if aws.ToString(pairs[0].Value) != "1" {
		t.Errorf("value for A = %q, want 1", aws.ToString(pairs[0].Value))
	}
}
