package exec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Request struct {
	Command string
	Args    []string
	Dir     string
	Stdin   string
	Timeout time.Duration
	// MaxOutputBytes caps captured stdout (the head is kept); 0 means "no
	// caller cap", which still stops at StdoutHardCap so a runaway CLI cannot
	// exhaust memory. Stderr is capped separately: see StderrCap.
	MaxOutputBytes int
	// StreamTo, if non-nil, means stdout and stderr are also written here live,
	// in addition to being captured in the Result.
	StreamTo io.Writer
	// Env holds extra KEY=VALUE entries appended to the inherited environment
	// (later entries win), e.g. to neutralise a tool's config lookups.
	Env []string
}

// StderrCap bounds captured stderr. Only its tail is kept: the CLIs write a
// verbose progress trace there and the error that matters comes last. It is
// independent of MaxOutputBytes, which callers set to 0 when stdout is a
// structured stream that must be parsed whole.
const StderrCap = 64 << 10

// StdoutHardCap bounds stdout when the caller sets no MaxOutputBytes (the
// structured JSON/JSONL streams that are parsed whole). Exceeding it sets
// StdoutTruncated, which those callers treat as an error.
const StdoutHardCap = 32 << 20

// headBuffer keeps the first max bytes written and discards the rest (still
// consuming them, so the child never blocks on a full pipe).
type headBuffer struct {
	max       int
	buf       bytes.Buffer
	truncated bool
}

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := h.max - h.buf.Len(); room < len(p) {
		if room > 0 {
			h.buf.Write(p[:room])
		}
		h.truncated = true
		return len(p), nil
	}
	h.buf.Write(p)
	return len(p), nil
}

type Result struct {
	Stdout string
	Stderr string
	// ExitCode is the process exit code; it is -1 when TimedOut is true.
	ExitCode int
	Elapsed  time.Duration
	TimedOut bool
	// StdoutTruncated reports that stdout exceeded MaxOutputBytes and was cut.
	StdoutTruncated bool
	// StderrTruncated reports that stderr exceeded StderrCap; Stderr then
	// holds only its last StderrCap bytes.
	StderrTruncated bool
}

// tailBuffer is an io.Writer that keeps only the last max bytes written.
type tailBuffer struct {
	max     int
	buf     []byte
	dropped bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	// Compact only once twice the cap is buffered, so trimming is amortised.
	if len(t.buf) > 2*t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
		t.dropped = true
	}
	return len(p), nil
}

func (t *tailBuffer) result() (string, bool) {
	if len(t.buf) > t.max {
		return string(t.buf[len(t.buf)-t.max:]), true
	}
	return string(t.buf), t.dropped
}

func Run(ctx context.Context, req Request) (Result, error) {
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	cmd := exec.Command(req.Command, req.Args...)
	cmd.Dir = req.Dir
	if len(req.Env) > 0 {
		cmd.Env = append(os.Environ(), req.Env...)
	}
	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	}
	setSysProcAttr(cmd)

	limit := req.MaxOutputBytes
	if limit <= 0 {
		limit = StdoutHardCap
	}
	out := headBuffer{max: limit}
	errb := tailBuffer{max: StderrCap}
	if req.StreamTo != nil {
		cmd.Stdout = io.MultiWriter(&out, req.StreamTo)
		cmd.Stderr = io.MultiWriter(&errb, req.StreamTo)
	} else {
		cmd.Stdout = &out
		cmd.Stderr = &errb
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var res Result
	select {
	case <-ctx.Done():
		killProcessGroup(cmd)
		<-done
		res.TimedOut = true
	case err := <-done:
		if err != nil {
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				return Result{}, err
			}
		}
	}
	res.Elapsed = time.Since(start)
	res.ExitCode = cmd.ProcessState.ExitCode()

	res.Stdout, res.StdoutTruncated = out.buf.String(), out.truncated
	res.Stderr, res.StderrTruncated = errb.result()
	return res, nil
}
