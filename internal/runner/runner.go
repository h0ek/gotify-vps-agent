package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/textsafe"
)

const maxOutput = 1024 * 1024

type Result struct {
	Output    string
	ExitCode  int
	Truncated bool
}

type limitedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	original := len(p)
	if w.remaining <= 0 {
		w.truncated = true
		return original, nil
	}
	if len(p) > w.remaining {
		p = p[:w.remaining]
		w.truncated = true
	}
	_, _ = w.buffer.Write(p)
	w.remaining -= len(p)
	return original, nil
}

func (w *limitedBuffer) result() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return textsafe.Sanitize(w.buffer.String(), maxOutput), w.truncated
}

func Run(ctx context.Context, timeout time.Duration, path string, args ...string) (Result, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
		"LC_ALL=C",
		"LANG=C",
		"DEBIAN_FRONTEND=noninteractive",
		"HOME=/root",
	}
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second

	output := &limitedBuffer{remaining: maxOutput}
	cmd.Stdout = output
	cmd.Stderr = output

	err := cmd.Run()
	text, truncated := output.result()
	result := Result{Output: strings.TrimSpace(text), ExitCode: 0, Truncated: truncated}
	if err == nil {
		return result, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return result, fmt.Errorf("command timed out after %s", timeout)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return result, fmt.Errorf("command not found: %s", path)
	}
	return result, err
}

func CopyLimited(dst io.Writer, src io.Reader, limit int64) (bool, error) {
	reader := &io.LimitedReader{R: src, N: limit + 1}
	written, err := io.Copy(dst, reader)
	if err != nil {
		return false, err
	}
	return written > limit, nil
}
