package exec

import (
	"context"
	"testing"
)

func TestRunnerAdapter(t *testing.T) {
	var r Runner
	res, err := r.Run(context.Background(), helper(t, "echo", "ok"))
	if err != nil || res.Stdout != "ok" {
		t.Errorf("adapter failed: %q %v", res.Stdout, err)
	}
}
