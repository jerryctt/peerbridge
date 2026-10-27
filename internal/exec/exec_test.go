package exec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tests re-execute this test binary as a helper process (the pattern
// os/exec itself uses), so they need no printf/cat/sh/sleep and run the same
// on Linux, macOS and Windows. The helper's behaviour is selected by argv:
//
//	echo <text>      write text to stdout
//	cat              copy stdin to stdout
//	env <NAME>       print the value of environment variable NAME
//	exit <code>      exit with code
//	sleep <seconds>  sleep (and ignore nothing — the test kills it)
func TestMain(m *testing.M) {
	if os.Getenv("PEERBRIDGE_EXEC_HELPER") != "1" {
		os.Exit(m.Run())
	}
	args := os.Args[1:]
	// Strip go test flags that precede our marker when re-exec'd.
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	switch args[0] {
	case "echo":
		fmt.Fprint(os.Stdout, strings.Join(args[1:], " "))
	case "cat":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "env":
		fmt.Fprint(os.Stdout, os.Getenv(args[1]))
	case "exit":
		code, _ := strconv.Atoi(args[1])
		os.Exit(code)
	case "sleep":
		secs, _ := strconv.Atoi(args[1])
		time.Sleep(time.Duration(secs) * time.Second)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

// helper returns a Request that runs this test binary in helper mode.
func helper(t *testing.T, args ...string) Request {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Request{
		Command:        exe,
		Args:           append([]string{"-test.run=^$", "--"}, args...),
		Env:            []string{"PEERBRIDGE_EXEC_HELPER=1"},
		MaxOutputBytes: 1000,
	}
}

func TestRunCapturesStdoutAndExit(t *testing.T) {
	r, err := Run(context.Background(), helper(t, "echo", "hello"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Stdout != "hello" || r.ExitCode != 0 {
		t.Errorf("got stdout=%q exit=%d", r.Stdout, r.ExitCode)
	}
}

func TestRunAppendsEnv(t *testing.T) {
	req := helper(t, "env", "PEERBRIDGE_TEST_VAR")
	req.Env = append(req.Env, "PEERBRIDGE_TEST_VAR=from-request")
	r, err := Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Stdout != "from-request" {
		t.Errorf("env not applied, stdout=%q", r.Stdout)
	}
}

func TestRunFeedsStdin(t *testing.T) {
	req := helper(t, "cat")
	req.Stdin = "piped-input"
	r, err := Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Stdout != "piped-input" {
		t.Errorf("stdin not forwarded, got %q", r.Stdout)
	}
}

func TestRunNonZeroExit(t *testing.T) {
	r, err := Run(context.Background(), helper(t, "exit", "3"))
	if err != nil {
		t.Fatalf("Run should not error on non-zero exit: %v", err)
	}
	if r.ExitCode != 3 {
		t.Errorf("exit = %d, want 3", r.ExitCode)
	}
}

func TestRunTimeoutKills(t *testing.T) {
	start := time.Now()
	req := helper(t, "sleep", "5")
	req.Timeout = 150 * time.Millisecond
	r, err := Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !r.TimedOut {
		t.Errorf("expected TimedOut")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("did not kill promptly")
	}
}

func TestRunTruncates(t *testing.T) {
	req := helper(t, "echo", "abcdefghij")
	req.MaxOutputBytes = 4
	r, err := Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !r.Truncated || len(r.Stdout) != 4 || !strings.HasPrefix("abcdefghij", r.Stdout) {
		t.Errorf("got %q truncated=%v", r.Stdout, r.Truncated)
	}
}

func TestRunStreamsToWriter(t *testing.T) {
	var stream bytes.Buffer
	req := helper(t, "echo", "hello")
	req.StreamTo = &stream
	r, err := Run(context.Background(), req)
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
