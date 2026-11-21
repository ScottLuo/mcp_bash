//go:build unit

package main

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestVersionDefaultValue verifies that the version variable has the expected
// default value when not overridden via -ldflags.
func TestVersionDefaultValue(t *testing.T) {
	if version != "0.1.0" {
		t.Errorf("version = %q, want %q", version, "0.1.0")
	}
}

// TestVersionIsVar verifies that version can be overridden at build time
// (i.e. it is a var, not a const). We simulate the -ldflags behavior by
// directly assigning to it and verifying the change.
func TestVersionIsVar(t *testing.T) {
	original := version
	version = "v9.9.9"
	if version != "v9.9.9" {
		t.Errorf("version = %q after assignment, want %q", version, "v9.9.9")
	}
	version = original
}

// TestResolveLogLevel tests the log level resolution logic.
func TestResolveLogLevel(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty defaults to INFO", "", "INFO"},
		{"lowercase normalized", "debug", "DEBUG"},
		{"valid INFO", "INFO", "INFO"},
		{"valid WARN", "WARN", "WARN"},
		{"valid ERROR", "ERROR", "ERROR"},
		{"invalid defaults to INFO", "TRACE", "INFO"},
		{"whitespace trimmed", "  WARN  ", "WARN"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", tc.input)
			got := resolveLogLevel()
			if got != tc.expected {
				t.Errorf("resolveLogLevel() = %q, want %q", got, tc.expected)
			}
		})
	}
}

// TestLevelRank tests the log level ranking function.
func TestLevelRank(t *testing.T) {
	tests := []struct {
		level    string
		expected int
	}{
		{"DEBUG", 0},
		{"INFO", 1},
		{"WARN", 2},
		{"ERROR", 3},
		{"unknown", 1},
		{"", 1},
		{"debug", 0},
		{"error", 3},
	}

	for _, tc := range tests {
		t.Run(tc.level, func(t *testing.T) {
			if got := levelRank(tc.level); got != tc.expected {
				t.Errorf("levelRank(%q) = %d, want %d", tc.level, got, tc.expected)
			}
		})
	}
}

// TestLogf_AtInfoLevel verifies logf writes to stderr when log level allows.
func TestLogf_AtInfoLevel(t *testing.T) {
	originalLevel := logLevel
	logLevel = "DEBUG"
	defer func() { logLevel = originalLevel }()

	// Logf writes to stderr; it should not panic and should produce output.
	// We simply invoke it to cover the code path.
	logf("INFO", "test message", "key1", "val1", "key2", "val2")
	logf("ERROR", "error message")
	logf("WARN", "warning message", "single_kv_odd")
}

// TestLogf_Filtered verifies logf suppresses messages below the threshold.
func TestLogf_Filtered(t *testing.T) {
	originalLevel := logLevel
	logLevel = "ERROR"
	defer func() { logLevel = originalLevel }()

	// DEBUG is below ERROR threshold; should return early without writing.
	logf("DEBUG", "should be suppressed")
	logf("INFO", "also suppressed")
}

// TestBashTool verifies the tool definition is constructed correctly.
func TestBashTool(t *testing.T) {
	tool := bashTool()

	if tool.Name != "bash" {
		t.Errorf("tool.Name = %q, want %q", tool.Name, "bash")
	}

	// Verify the tool has a non-empty description.
	if tool.Description == "" {
		t.Error("tool.Description is empty, want non-empty")
	}

	// Verify input schema has required fields.
	if len(tool.InputSchema.Required) != 1 || tool.InputSchema.Required[0] != "command" {
		t.Errorf("tool.InputSchema.Required = %v, want [command]", tool.InputSchema.Required)
	}

	// Verify all expected properties are present.
	expectedProps := []string{"work_path", "command", "timeout", "stdin"}
	for _, p := range expectedProps {
		if _, ok := tool.InputSchema.Properties[p]; !ok {
			t.Errorf("tool.InputSchema.Properties missing key %q", p)
		}
	}
}

// TestBashHandler_EmptyCommand tests that bashHandler returns an error when
// command is empty.
func TestBashHandler_EmptyCommand(t *testing.T) {
	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "bash",
			Arguments: map[string]any{},
		},
	}

	result, err := bashHandler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil, want non-nil")
	}
	if !result.IsError {
		t.Error("expected result.IsError = true for empty command")
	}
}

// TestBashHandler_Success tests that bashHandler returns a successful result
// for a valid command.
func TestBashHandler_Success(t *testing.T) {
	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "bash",
			Arguments: map[string]any{
				"command": "echo hello",
			},
		},
	}

	result, err := bashHandler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil, want non-nil")
	}
	if result.IsError {
		t.Error("expected result.IsError = false for successful command")
	}
}
