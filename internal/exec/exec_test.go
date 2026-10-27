package exec

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesStdoutAndExit(t *testing.T) {
	r, err := Run(context.Background(), Request{
		Command: "printf", Args: []string{"hello"}, MaxOutputBytes: 1000,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Stdout != "hello" || r.ExitCode != 0 {
		t.Errorf("got stdout=%q exit=%d", r.Stdout, r.ExitCode)
	}
}

func TestRunAppendsEnv(t *testing.T) {
	r, err := Run(context.Background(), Request{
		Command: "sh", Args: []string{"-c", "echo $HOSTRUNNER_TEST_VAR"}, Env: []string{"HOSTRUNNER_TEST_VAR=from-request"}, MaxOutputBytes: 1000,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(r.Stdout) != "from-request" {
		t.Errorf("env not applied, stdout=%q", r.Stdout)
	}
}

func TestRunFeedsStdin(t *testing.T) {
	r, err := Run(context.Background(), Request{
		Command: "cat", Stdin: "piped-input", MaxOutputBytes: 1000,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Stdout != "piped-input" {
		t.Errorf("stdin not forwarded, got %q", r.Stdout)
	}
}

func TestRunNonZeroExit(t *testing.T) {
	r, err := Run(context.Background(), Request{
		Command: "sh", Args: []string{"-c", "exit 3"}, MaxOutputBytes: 1000,
	})
	if err != nil {
		t.Fatalf("Run should not error on non-zero exit: %v", err)
	}
	if r.ExitCode != 3 {
		t.Errorf("exit = %d, want 3", r.ExitCode)
	}
}

func TestRunTimeoutKills(t *testing.T) {
	start := time.Now()
	r, err := Run(context.Background(), Request{
		Command: "sleep", Args: []string{"5"}, Timeout: 150 * time.Millisecond, MaxOutputBytes: 1000,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !r.TimedOut {
		t.Errorf("expected TimedOut")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("did not kill promptly")
	}
}

func TestRunTruncates(t *testing.T) {
	r, err := Run(context.Background(), Request{
		Command: "printf", Args: []string{"abcdefghij"}, MaxOutputBytes: 4,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !r.Truncated || len(r.Stdout) != 4 || !strings.HasPrefix("abcdefghij", r.Stdout) {
		t.Errorf("got %q truncated=%v", r.Stdout, r.Truncated)
	}
}

func TestRunStreamsToWriter(t *testing.T) {
	var stream bytes.Buffer
	r, err := Run(context.Background(), Request{
		Command:        "printf",
		Args:           []string{"hello"},
		StreamTo:       &stream,
		MaxOutputBytes: 1000,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Stdout != "hello" {
		t.Errorf("Result.Stdout = %q, want hello", r.Stdout)
	}
	if stream.String() != "hello" {
		t.Errorf("stream buffer = %q, want hello", stream.String())
	}
}
