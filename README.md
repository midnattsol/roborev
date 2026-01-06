# roborev

Automatic code review for git commits using AI agents.

![TUI Queue View](docs/screenshots/tui-queue.png)

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/wesm/roborev/main/scripts/install.sh | bash
```

Or with Go:

```bash
go install github.com/wesm/roborev/cmd/roborev@latest
go install github.com/wesm/roborev/cmd/roborevd@latest
```

Make sure `$GOPATH/bin` is in your PATH. Add to your shell config (e.g., `~/.zshrc`):

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

Then restart your shell or run `source ~/.zshrc`.

## Quick Start

```bash
cd your-repo
roborev init
```

This installs a post-commit hook. Every commit is now reviewed automatically.

## Usage

```bash
roborev status       # Show queue and daemon status
roborev show         # Show review for HEAD
roborev show abc123  # Show review for specific commit
roborev respond HEAD # Add response to a review
roborev tui          # Interactive terminal UI
```

### Reviewing Commit Ranges

Review multiple commits at once:

```bash
roborev enqueue abc123 def456   # Review commits from abc123 to def456 (inclusive)
```

The range is inclusive of both endpoints. This is useful for reviewing a feature branch or a set of related commits together.

## Configuration

Per-repository `.roborev.toml`:

```toml
agent = "claude-code"    # or "codex"
review_context_count = 5
```

Global `~/.roborev/config.toml`:

```toml
server_addr = "127.0.0.1:7373"
max_workers = 4
default_agent = "codex"
```

## Architecture

roborev runs as a local daemon that processes review jobs in parallel.

```
~/.roborev/
├── config.toml    # Configuration
├── daemon.json    # Runtime state (port, PID)
└── reviews.db     # SQLite database
```

The daemon starts automatically when needed and handles port conflicts by finding an available port.

## Agents

roborev supports multiple AI review agents:

- `codex` - OpenAI Codex CLI
- `claude-code` - Anthropic Claude Code CLI

### Automatic Fallback

roborev automatically detects which agents are installed and falls back gracefully:

- If `codex` is requested but not installed, roborev uses `claude` instead
- If `claude-code` is requested but not installed, roborev uses `codex` instead
- If neither is installed, the job fails with a helpful error message

### Explicit Agent Selection

To use a specific agent for a repository, create `.roborev.toml` in the repo root:

```toml
agent = "claude-code"
```

Or set a global default in `~/.roborev/config.toml`:

```toml
default_agent = "claude-code"
```

### Selection Priority

1. `--agent` flag on enqueue command
2. Per-repo `.roborev.toml`
3. Global `~/.roborev/config.toml`
4. Automatic detection (uses first available: codex, claude-code)

## Commands

| Command | Description |
|---------|-------------|
| `roborev init` | Initialize in current repo |
| `roborev status` | Show daemon and queue status |
| `roborev show [sha]` | Display review |
| `roborev respond <sha>` | Add response |
| `roborev enqueue [commit]` | Enqueue a single commit for review |
| `roborev enqueue <start> <end>` | Enqueue a commit range for review |
| `roborev daemon start\|stop\|restart` | Manage daemon |
| `roborev install-hook` | Install git hook only |
| `roborev tui` | Interactive terminal UI |

## Development

```bash
git clone https://github.com/wesm/roborev
cd roborev
go test ./...
go install ./cmd/...
```

## License

MIT
