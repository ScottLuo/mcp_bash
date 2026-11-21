# mcp_bash

A Model Context Protocol (MCP) server written in Go that exposes a single tool — `bash` — allowing an AI agent to execute arbitrary shell commands in a specified working directory, with configurable timeout, optional stdin, automatic output compression, and size-based truncation.

The server speaks MCP over **stdio** (JSON-RPC): it reads requests from standard input and writes responses to standard output. All human-facing log output is written to **stderr**, keeping stdout clean for the protocol.

## Features

- **MCP stdio transport** — compliant JSON-RPC over stdin/stdout, driven by [`github.com/mark3labs/mcp-go`](https://github.com/mark3labs/mcp-go).
- **`bash` tool** — runs `bash -c <command>` inside a chosen working directory.
- **Timeout enforcement** — a `context` deadline; on expiry the entire process group receives `SIGTERM`, then `SIGKILL` after 100ms, so no child processes are left behind.
- **Process-group isolation** — commands run with `Setpgid`, enabling whole-group signalling.
- **Output compression** — detects and compresses three common output formats:
  - `go test -v` (merges PASS subtests, preserves FAIL detail),
  - `grep`/`rg` (groups by file, merges duplicate line content),
  - `find`/`ls` path lists (extracts common prefix, groups by directory).
- **Output truncation** — stdout/stderr capped at 32KB with a `[output truncated]` marker.
- **Structured result** — `stdout`, `stderr`, `return_code`, `timed_out`, `truncated`, `duration` (µs).
- **Graceful shutdown** — exits cleanly on `SIGINT`/`SIGTERM` or EOF on stdin.

## Tool Input Schema

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `work_path` | string | no | Working directory for execution. Defaults to `.`. |
| `command` | string | **yes** | The shell command to run via `bash -c`. |
| `timeout` | integer | no | Max execution time in seconds. Defaults to 30. |
| `stdin` | string | no | Data to feed into the command's standard input. |

## Tool Output

The tool returns a JSON object:

```json
{
  "stdout": "hello\n",
  "stderr": "",
  "return_code": 0,
  "timed_out": false,
  "truncated": false,
  "duration": 1234
}
```

## Configuration

Configuration is read from environment variables at startup.

| Variable | Default | Description |
|----------|---------|-------------|
| `LOG_LEVEL` | `INFO` | Log verbosity (`DEBUG`, `INFO`, `WARN`, `ERROR`). Unrecognized values fall back to `INFO` with a warning. |

## Building

The build targets **linux/amd64** with CGO disabled for a static, portable binary.

```bash
# Cross-compile for linux/amd64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/mcp_bash .

# Verify the binary
file bin/mcp_bash
# Expected: ELF 64-bit LSB executable, x86-64, ...
```

## Packaging for Release

The release archive must contain the compiled binary and `tool.yaml`.

```bash
mkdir -p release/
cp bin/mcp_bash release/
cp tool.yaml release/
chmod +x release/mcp_bash
tar -czf mcp_bash-linux-amd64.tar.gz -C release .
```

Resulting package contents:

```
mcp_bash-linux-amd64.tar.gz
├── mcp_bash      # compiled linux/amd64 executable
└── tool.yaml     # runtime contract
```

## Usage

Run the binary; it reads MCP JSON-RPC messages from stdin.

### Example: initialize

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"client","version":"0.1.0"}}}
```

### Example: list tools

```json
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
```

### Example: run `echo hello`

```json
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"bash","arguments":{"command":"echo hello"}}}
```

### Example: with timeout

```json
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"bash","arguments":{"command":"sleep 10","timeout":1}}}
```

## Running Tests

```bash
# Unit tests (table-driven, build tag `unit`)
go test -tags=unit -v ./... --timeout 20s

# Unit tests with race detector
go test -tags=unit -race ./... --timeout 60s

# Static checks
golangci-lint run
```

## Project Layout

```
.
├── go.mod / go.sum
├── main.go          # entry point: MCP stdio server + bash tool registration
├── cmd/
│   ├── bash.go      # Bash execution, CommandResponse, truncation
│   ├── compress.go  # AutoCompressShellOutput + sub-functions
│   ├── bash_test.go # unit tests for Bash
│   └── compress_test.go # unit tests for compression
├── tool.yaml        # runtime contract
└── README.md
```
