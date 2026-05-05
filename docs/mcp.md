# MCP Server

> **STATUS: design / not yet shipped.** This doc describes the planned
> shape of the MCP integration; `sparkwing mcp serve` and the
> `wing tools` surface are not implemented in the current binary.
> Until then, agents should drive sparkwing via `sparkwing commands
> --json` + `sparkwing pipeline {list,describe,explain} --json` +
> `sparkwing pipeline run`.

Sparkwing will include an MCP (Model Context Protocol) server that exposes pipeline commands to AI agents through a single, context-efficient tool.

## The Problem

Standard MCP servers dump all tool definitions into the agent's context. 50 tools = 10,000+ tokens wasted before the conversation starts.

## The Solution

Sparkwing exposes **one MCP tool** with hierarchical discovery:

```
sparkwing(pipeline="list")                              → namespaces
sparkwing(pipeline="list", command="pipeline")           → commands
sparkwing(pipeline="schema", command="pipeline.release") → JSON Schema
sparkwing(pipeline="run", command="pipeline.release", args={version: "v1.0"}) → execute
```

~100 tokens in context instead of 10,000+.

## Starting the Server

```bash
sparkwing mcp serve
```

This starts a JSON-RPC 2.0 server over stdio — the standard MCP transport.

## Configuration

Add to your AI tool's MCP config:

```json
{
  "sparkwing": {
    "command": "sparkwing",
    "args": ["mcp", "serve"]
  }
}
```

For Claude Code, add to `.mcp.json` in your project root.

## How Agents Use It

### 1. Discover Namespaces

```json
{"pipeline": "list"}
→ {"namespaces": [{"name": "pipeline", "commands": 5}]}
```

### 2. List Commands

```json
{"pipeline": "list", "command": "pipeline"}
→ {"commands": [
    {"name": "build-deploy", "help": "Build all apps and deploy via gitops"},
    {"name": "release", "help": "Tag, build, and publish a release"}
  ]}
```

### 3. Get Schema

```json
{"pipeline": "schema", "command": "pipeline.build-deploy"}
→ {
    "input_schema": {"type": "object", "properties": {...}},
    "output_schema": {"type": "object", "properties": {...}},
    "example": {"apps": ["myapp"], "commit": "abc123"},
    "errors": [{"code": "BUILD_FAILED", ...}]
  }
```

### 4. Execute

```json
{"pipeline": "run", "command": "pipeline.build-deploy", "args": {"apps": "myapp"}}
→ {"ok": true, "data": {"apps": ["myapp"], "commit": "abc123"}}
```

### 5. Read Documentation

```json
{"pipeline": "docs", "command": "pipeline-guide"}
→ (markdown content of the doc)
```

### 6. Generate Pipeline Code

```json
{"pipeline": "generate", "command": "docker-deploy", "args": {"image": "myapp"}}
→ (generated Go code for a Docker deploy pipeline)
```

## Tool Definition

What the agent sees in its context (~100 tokens):

```json
{
  "name": "sparkwing",
  "description": "CI/CD and infrastructure tool. Pipelines: list, schema, run, docs, generate.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "pipeline": {"type": "string", "enum": ["list", "schema", "run", "docs", "generate"]},
      "command": {"type": "string"},
      "args": {"type": "object"}
    },
    "required": ["pipeline"]
  }
}
```

## Future: Tool Aggregation

Sparkwing can aggregate external MCP servers into namespaces:

```bash
wing tools                          # → pipeline, git, linear, github
wing tools github                   # → create-issue, search-repos, ...
wing tools github create-issue --schema
wing tools github create-issue --json --arg title="Fix bug"
```

External tools configured in `.sparkwing/tools.yaml` are proxied through sparkwing's governance layer (validation, audit, rate limiting).
