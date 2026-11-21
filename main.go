// Command mcp_bash is a Model Context Protocol (MCP) server that exposes a
// single tool, "bash", which executes a shell command in a given working
// directory with a configurable timeout and optional stdin. It communicates
// over stdio (JSON-RPC), reading requests from stdin and writing responses to
// stdout.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	bashcmd "mcp_bash/cmd"
)

// version is the semantic version of the mcp_bash server.
// It can be overridden at build time via:
//   -ldflags "-X main.version=vX.Y.Z"
var version = "0.1.0"

// validLogLevels is the set of accepted LOG_LEVEL values.
var validLogLevels = map[string]bool{
	"DEBUG": true,
	"INFO":  true,
	"WARN":  true,
	"ERROR": true,
}

// levelRank orders log levels so we can filter. Higher means more important.
func levelRank(level string) int {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return 0
	case "INFO":
		return 1
	case "WARN":
		return 2
	case "ERROR":
		return 3
	default:
		return 1
	}
}

// logLevel is the effective log level (normalized to upper-case).
var logLevel = "INFO"

// logf writes a structured log line to stderr in the form:
//
//	[LEVEL] 2006-01-02T15:04:05Z07:00 message key=value key=value
//
// stdout is reserved for MCP JSON-RPC, so all log output goes to stderr.
func logf(level, msg string, kv ...any) {
	if levelRank(level) < levelRank(logLevel) {
		return
	}
	var b strings.Builder
	b.WriteString("[" + level + "] ")
	b.WriteString(time.Now().UTC().Format(time.RFC3339))
	b.WriteString(" " + msg)
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteString(" ")
		fmt.Fprint(&b, kv[i])
		b.WriteString("=")
		fmt.Fprint(&b, kv[i+1])
	}
	b.WriteString("\n")
	_, _ = os.Stderr.WriteString(b.String())
}

// resolveLogLevel reads LOG_LEVEL from the environment, validates it, and
// returns the normalized level, warning and defaulting to INFO when the value
// is unrecognized.
func resolveLogLevel() string {
	raw := strings.ToUpper(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	if raw == "" {
		return "INFO"
	}
	if !validLogLevels[raw] {
		logf("WARN", "unrecognized LOG_LEVEL, defaulting to INFO", "log_level", raw)
		return "INFO"
	}
	return raw
}

// bashArgs mirrors the bash tool input schema.
type bashArgs struct {
	WorkPath string `json:"work_path"`
	Command  string `json:"command"`
	Timeout  int64  `json:"timeout"`
	Stdin    string `json:"stdin"`
}

// bashTool builds the mcp.Tool definition for the "bash" tool. The input
// schema matches the specification: only "command" is required.
func bashTool() mcp.Tool {
	tool := mcp.NewTool("bash",
		mcp.WithDescription("Execute a bash command in the specified working directory with timeout and optional stdin. Returns stdout, stderr, return code, duration, and truncation status. Output is automatically compressed for readability."),
	)
	tool.InputSchema = mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"work_path": map[string]any{
				"type":        "string",
				"description": "Working directory for command execution. Defaults to current directory.",
			},
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute via bash -c.",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Maximum execution time in seconds. Defaults to 30.",
			},
			"stdin": map[string]any{
				"type":        "string",
				"description": "Optional data to feed into the command's standard input.",
			},
		},
		Required: []string{"command"},
	}
	return tool
}

// bashHandler is the tool handler for the "bash" tool. It binds the request
// arguments, runs the command, and returns a structured result. Panics inside
// the underlying Bash call are recovered there and surfaced as an error result.
func bashHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args bashArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to bind arguments: %v", err)), nil
	}

	// command is required by the schema; if a client omits it, BindArguments
	// leaves it empty. Enforce it here as well.
	if args.Command == "" {
		return mcp.NewToolResultError("command cannot be empty"), nil
	}

	logf("INFO", "executing bash tool",
		"command", args.Command,
		"work_path", args.WorkPath,
		"timeout", args.Timeout,
	)

	resp, err := bashcmd.Bash(ctx, args.WorkPath, args.Command, args.Timeout, args.Stdin)
	if err != nil {
		// The Bash function surfaces predictable failures in the response; a
		// non-nil error means an unexpected issue (e.g. a recovered panic).
		logf("ERROR", "bash tool failed", "error", err.Error())
		return mcp.NewToolResultError(fmt.Sprintf("bash execution failed: %v", err)), nil
	}

	logf("INFO", "bash tool finished",
		"return_code", resp.ReturnCode,
		"timed_out", resp.TimedOut,
		"duration_us", resp.Duration,
	)

	// NewToolResultJSON sets both a JSON text content and the structured
	// content, so clients receive the result in both forms.
	return mcp.NewToolResultJSON(resp)
}

func main() {
	logLevel = resolveLogLevel()

	// Set up signal handling so we can shut down gracefully on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.NewMCPServer("mcp_bash", version)

	// Register the bash tool with its handler.
	srv.AddTool(bashTool(), bashHandler)

	// Use the stdio transport. NewStdioServer wires up the session and the
	// worker pool; Listen drives the request loop over stdin/stdout and returns
	// when stdin is closed (EOF) or the context is cancelled.
	stdio := server.NewStdioServer(srv)

	logf("INFO", "mcp_bash server starting",
		"name", "mcp_bash",
		"version", version,
		"log_level", logLevel,
	)

	err := stdio.Listen(ctx, os.Stdin, os.Stdout)

	switch {
	case err == nil:
		// stdin reached EOF; clean shutdown.
		logf("INFO", "mcp_bash server stopped cleanly")
	case strings.Contains(err.Error(), "context canceled"):
		// Terminated by a signal.
		logf("INFO", "mcp_bash server stopped", "reason", "signal")
	default:
		logf("ERROR", "mcp_bash server error", "error", err.Error())
		os.Exit(1)
	}
}
