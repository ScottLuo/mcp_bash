package bash

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"syscall"
	"time"
)

// MaxOutputSize is the maximum size (in bytes) of a single stdout/stderr
// capture before it is truncated. The constant is intentionally exported so
// callers and tests can reason about the limit.
const MaxOutputSize = 32 * 1024

// truncateMarker is appended to truncated output so a consumer can tell that
// the value was cut.
const truncateMarker = "\n[output truncated]"

// CommandResponse is the structured result returned by the bash tool.
type CommandResponse struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ReturnCode int    `json:"return_code"`
	TimedOut   bool   `json:"timed_out"`
	Truncated  bool   `json:"truncated"`
	Duration   int64  `json:"duration"` // microseconds
}

// truncateOutput limits a string to MaxOutputSize bytes and reports whether it
// was truncated. If truncation happens, a human-readable marker is appended so
// that downstream consumers (typically an LLM) know the value is incomplete.
func truncateOutput(s string) (string, bool) {
	if len(s) <= MaxOutputSize {
		return s, false
	}
	// Avoid splitting a multi-byte UTF-8 rune at the cut boundary.
	cut := safeByteCut(s, MaxOutputSize)
	return s[:cut] + truncateMarker, true
}

// safeByteCut returns a byte offset at or before n that aligns to a UTF-8
// rune boundary. It is a helper that keeps the truncation from producing a
// replacement character when the source contains multi-byte characters.
func safeByteCut(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	// Scan backward from n to the start of the rune containing byte n.
	// Continuation bytes in UTF-8 have the 0b10xxxxxx (0x80..0xBF) pattern.
	i := n
	for i > 0 && s[i]&0xC0 == 0x80 {
		i--
	}
	return i
}

// Bash executes a shell command inside the given working directory with the
// supplied timeout and optional stdin, returning a structured result. It is
// the core of the MCP "bash" tool.
//
// The implementation:
//  1. Validates the working directory and the command string.
//  2. Runs `bash -c <command>` in its own process group (Setpgid) so that
//     all child processes can be killed on timeout.
//  3. Enforces the timeout via a context and, on expiry, signals SIGTERM to
//     the whole process group, waits 100ms, and signals SIGKILL.
//  4. Applies output compression (AutoCompressShellOutput) and truncation
//     to both stdout and stderr.
//
// It returns the CommandResponse and an optional error. The error is only
// non-nil for unexpected failures (e.g. panics surfaced via recoverPanic or
// marshaling errors); predictable conditions are represented inside the
// CommandResponse (non-zero ReturnCode, TimedOut, etc.).
func Bash(ctx context.Context, workPath, command string, timeout int64, stdin string) (resp *CommandResponse, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
			resp = &CommandResponse{
				Stderr:     fmt.Sprintf("internal panic: %v", r),
				ReturnCode: -1,
			}
		}
	}()

	startTime := time.Now()

	// Default working directory.
	if workPath == "" {
		workPath = "."
	}

	// Validate the working directory.
	if _, statErr := os.Stat(workPath); statErr != nil {
		return &CommandResponse{
			Stderr:     fmt.Sprintf("failed to access work_path '%s': %v", workPath, statErr),
			ReturnCode: -1,
			Duration:   time.Since(startTime).Microseconds(),
		}, nil
	}

	// Validate the command.
	if command == "" {
		return &CommandResponse{
			Stderr:     "command cannot be empty",
			ReturnCode: -1,
			Duration:   time.Since(startTime).Microseconds(),
		}, nil
	}

	// Timeout defaults to 30 seconds.
	timeoutDur := time.Duration(timeout) * time.Second
	if timeoutDur == 0 {
		timeoutDur = 30 * time.Second
	}

	// Derive a timeout context. If the caller's context is already canceled
	// (e.g. the server is shutting down after stdin EOF), fall back to
	// context.Background() so the command still gets its full timeout window.
	baseCtx := ctx
	if ctx.Err() != nil {
		baseCtx = context.Background()
	}
	timeoutCtx, cancel := context.WithTimeout(baseCtx, timeoutDur)
	defer cancel()

	// Use bash -c with Setpgid so the command runs in its own process group.
	// This lets us signal the whole group on timeout, killing any child
	// processes spawned by the command.
	cmd := exec.CommandContext(timeoutCtx, "bash", "-c", command)
	cmd.Dir = workPath
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if stdin != "" {
		cmd.Stdin = bytes.NewBufferString(stdin)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	// Start the process in the main goroutine so that cmd.Process is published
	// before any concurrent read (avoiding a data race on cmd.Process). Starting
	// here also lets us capture the pid synchronously for the kill logic below.
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return &CommandResponse{
				Stderr:     fmt.Sprintf("command not found: %v", err),
				ReturnCode: -1,
				Duration:   time.Since(startTime).Microseconds(),
			}, nil
		}
		return &CommandResponse{
			Stderr:     fmt.Sprintf("failed to start command: %v", err),
			ReturnCode: -1,
			Duration:   time.Since(startTime).Microseconds(),
		}, nil
	}
	pid := cmd.Process.Pid

	// Wait for the command to finish in a goroutine; the result is delivered via
	// a channel so we can race it against the context cancellation.
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var runErr error
	select {
	case runErr = <-done:
		// Command completed (or was killed by exec.CommandContext).
	case <-timeoutCtx.Done():
		// Timeout or external cancellation fired first.
	}

	timedOut := timeoutCtx.Err() == context.DeadlineExceeded

	// If we timed out, kill the whole process group (the bash process plus any
	// children it spawned) so no child is left behind. SIGTERM first for a
	// graceful window, then SIGKILL to force it.
	if timedOut {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		time.Sleep(100 * time.Millisecond)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		// Block until cmd.Wait() returns so the pipe-copy goroutines spawned by
		// Start() have finished writing to stdoutBuf/stderrBuf. This makes it
		// safe to read those buffers below.
		<-done
	}

	// Compress then truncate both captures.
	stdoutStr := AutoCompressShellOutput(stdoutBuf.String())
	stderrStr := AutoCompressShellOutput(stderrBuf.String())

	dur := func() int64 { return time.Since(startTime).Microseconds() }

	// Timeout response.
	if timedOut {
		so, soTrunc := truncateOutput(stdoutStr)
		se, seTrunc := truncateOutput(stderrStr)
		return &CommandResponse{
			TimedOut:   true,
			Stdout:     so,
			Stderr:     se,
			ReturnCode: -1,
			Truncated:  soTrunc || seTrunc,
			Duration:   dur(),
		}, nil
	}

	// Unexpected runtime errors (non-exit, non-notfound).
	if runErr != nil {
		if errors.Is(runErr, exec.ErrNotFound) {
			return &CommandResponse{
				Stderr:     fmt.Sprintf("command not found: %v", runErr),
				ReturnCode: -1,
				Duration:   dur(),
			}, nil
		}

		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode := -1
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			}
			so, soTrunc := truncateOutput(stdoutStr)
			se, seTrunc := truncateOutput(stderrStr)
			return &CommandResponse{
				TimedOut:   false,
				Stdout:     so,
				Stderr:     se,
				ReturnCode: exitCode,
				Truncated:  soTrunc || seTrunc,
				Duration:   dur(),
			}, nil
		}

		return &CommandResponse{
			Stderr:     fmt.Sprintf("command execution failed: %v", runErr),
			ReturnCode: -1,
			Duration:   dur(),
		}, nil
	}

	// Successful execution.
	returnCode := 0
	if cmd.ProcessState != nil {
		returnCode = cmd.ProcessState.ExitCode()
	}
	so, soTrunc := truncateOutput(stdoutStr)
	se, seTrunc := truncateOutput(stderrStr)
	return &CommandResponse{
		TimedOut:   false,
		Stdout:     so,
		Stderr:     se,
		ReturnCode: returnCode,
		Truncated:  soTrunc || seTrunc,
		Duration:   dur(),
	}, nil
}
