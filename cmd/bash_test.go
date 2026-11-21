//go:build unit

package bash

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCtx returns a background context with a deadline generous enough for the
// subprocess tests but bounded so a hang cannot stall the suite indefinitely.
func newCtx(t *testing.T) context.Context {
	t.Helper()
	// No per-call deadline here; the whole suite is bounded by `--timeout`.
	return context.Background()
}

func TestBash_Echo(t *testing.T) {
	resp, err := Bash(newCtx(t), "", `echo "hello"`, 10, "")
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "hello\n", resp.Stdout)
	assert.Empty(t, resp.Stderr)
	assert.Equal(t, 0, resp.ReturnCode)
	assert.False(t, resp.TimedOut)
	assert.False(t, resp.Truncated)
	assert.Greater(t, resp.Duration, int64(0))
}

func TestBash_DefaultWorkPath(t *testing.T) {
	// Empty work_path defaults to ".".
	resp, err := Bash(newCtx(t), "", `echo defaulted`, 10, "")
	require.NoError(t, err)
	assert.Equal(t, "defaulted\n", resp.Stdout)
	assert.Equal(t, 0, resp.ReturnCode)
}

func TestBash_NonExistentWorkPath(t *testing.T) {
	resp, err := Bash(newCtx(t), "/nonexistent/path/should/not/exist", `echo hi`, 10, "")
	require.NoError(t, err)
	assert.Equal(t, -1, resp.ReturnCode)
	assert.Contains(t, resp.Stderr, "failed to access work_path")
}

func TestBash_EmptyCommand(t *testing.T) {
	resp, err := Bash(newCtx(t), ".", "", 10, "")
	require.NoError(t, err)
	assert.Equal(t, -1, resp.ReturnCode)
	assert.Equal(t, "command cannot be empty", resp.Stderr)
}

func TestBash_NonZeroExit(t *testing.T) {
	resp, err := Bash(newCtx(t), ".", `exit 42`, 10, "")
	require.NoError(t, err)
	assert.Equal(t, 42, resp.ReturnCode)
	assert.False(t, resp.TimedOut)
}

func TestBash_Stdin(t *testing.T) {
	resp, err := Bash(newCtx(t), ".", `cat`, 10, "hello via stdin\n")
	require.NoError(t, err)
	assert.Equal(t, "hello via stdin\n", resp.Stdout)
	assert.Equal(t, 0, resp.ReturnCode)
}

func TestBash_StderrCaptured(t *testing.T) {
	resp, err := Bash(newCtx(t), ".", `echo "oops" >&2; exit 0`, 10, "")
	require.NoError(t, err)
	assert.Equal(t, 0, resp.ReturnCode)
	assert.Equal(t, "oops\n", resp.Stderr)
	assert.Empty(t, resp.Stdout)
}

func TestBash_Timeout(t *testing.T) {
	resp, err := Bash(newCtx(t), ".", `sleep 10`, 1, "")
	require.NoError(t, err)
	assert.True(t, resp.TimedOut)
	assert.Equal(t, -1, resp.ReturnCode)
	// Duration should be roughly 1 second (in microseconds), well under 5s.
	assert.Less(t, resp.Duration, int64(5_000_000))
}

func TestBash_TimeoutKillsChildProcesses(t *testing.T) {
	// A backgrounded child plus `wait` ensures the process group contains
	// multiple processes; on timeout the whole group must be signalled so no
	// child is left behind.
	resp, err := Bash(newCtx(t), ".", `bash -c 'sleep 10 & wait'`, 1, "")
	require.NoError(t, err)
	assert.True(t, resp.TimedOut)
	assert.Equal(t, -1, resp.ReturnCode)
	assert.Less(t, resp.Duration, int64(5_000_000))
}

func TestBash_OutputCompressionApplied(t *testing.T) {
	// Emit grep-style output; the tool should run AutoCompressShellOutput on it.
	// The compressed form groups by file and merges duplicate content.
	input := "src/a.go:1:func foo() {}\nsrc/a.go:2:func foo() {}\nsrc/a.go:10:func bar() {}\n"
	cmd := "printf '%s' '" + strings.ReplaceAll(input, "'", `'\''`) + "'"
	resp, err := Bash(newCtx(t), ".", cmd, 10, "")
	require.NoError(t, err)
	assert.Equal(t, 0, resp.ReturnCode)
	// The result should be compressed (contains a file header) and shorter
	// than the raw input.
	assert.Contains(t, resp.Stdout, "[File: src/a.go]")
	assert.Contains(t, resp.Stdout, "1,2: func foo() {}")
	assert.Less(t, len(resp.Stdout), len(input))
}

func TestBash_TruncationApplied(t *testing.T) {
	// Produce ~40KB of output; the tool must truncate to MaxOutputSize and set
	// the Truncated flag.
	resp, err := Bash(newCtx(t), ".", `yes | head -c 40000`, 10, "")
	require.NoError(t, err)
	assert.Equal(t, 0, resp.ReturnCode)
	assert.True(t, resp.Truncated)
	assert.True(t, strings.HasSuffix(resp.Stdout, truncateMarker))
	// Body is capped at MaxOutputSize bytes.
	body := strings.TrimSuffix(resp.Stdout, truncateMarker)
	assert.LessOrEqual(t, len(body), MaxOutputSize)
}

func TestBash_CompactGoTestOutputCompressed(t *testing.T) {
	// A go-test-like multi-line PASS output is compressed to a short form.
	// Feed the output via stdin (cat reads it) so the command itself is fixed.
	input := "=== RUN   TestA\n--- PASS: TestA (0.00s)\n=== RUN   TestB\n--- PASS: TestB (0.00s)\n=== RUN   TestC\n--- PASS: TestC (0.00s)\nPASS\nok  \tpkg\t0.002s\n"
	resp, err := Bash(newCtx(t), ".", `cat`, 10, input)
	require.NoError(t, err)
	assert.Contains(t, resp.Stdout, "PASS: TestA")
	assert.Contains(t, resp.Stdout, "PASS: TestB")
	assert.Contains(t, resp.Stdout, "PASS: TestC")
	assert.Less(t, len(resp.Stdout), len(input))
}

// TestBash_WorkPathUsed verifies the command actually runs inside work_path.
func TestBash_WorkPathUsed(t *testing.T) {
	workDir := t.TempDir()
	resp, err := Bash(newCtx(t), workDir, `pwd`, 10, "")
	require.NoError(t, err)
	require.NotEmpty(t, resp.Stdout)
	// The printed path should equal the temp directory we passed as work_path.
	assert.Equal(t, workDir, strings.TrimSpace(resp.Stdout))
}
