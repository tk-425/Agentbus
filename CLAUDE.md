# agentbus

**Version:** v0.2.0

A local multi-agent message bus that lets any running AI coding agent send and receive
messages from any other running agent in real time, via tmux and a local broker.

---

## Tech Stack

- **Language:** Go
- **CLI framework:** cobra (`github.com/spf13/cobra`)
- **Multiplexer backends:** tmux and herdr (auto-detected via `HERDR_ENV=1` or `herdr` in PATH)
- **Database:** SQLite via `modernc.org/sqlite` (pure Go, no CGo) — shared agent registry + message history
- **Release:** GoReleaser

---

## Project Structure

```
main.go           # Entry point — calls cmd.Execute()
cmd/
├── root.go       # Root cobra command + Execute()
└── commands.go   # All subcommand stubs
Makefile          # build / install / uninstall targets
go.mod
go.sum
```

---

## Build & Install

```bash
make build        # compiles binary to ./agentbus
make install      # builds and moves binary to /usr/local/bin/agentbus
make uninstall    # removes /usr/local/bin/agentbus
```

---

## CLI Commands

```
agentbus start                        # start broker in background (auto-discovers agents)
agentbus stop                         # stop broker
agentbus register --name <agent>      # register current tmux pane (auto-suffixes if name taken)
agentbus unregister --name <agent>    # remove agent from registry
agentbus send --to <agent> <message>  # send message to agent
agentbus inbox [--wait] [--timeout]   # read inbox (marks as read; --wait blocks until message arrives)
agentbus list                         # list registered agents
agentbus status                       # show statusline data
agentbus log                          # show recent message history
agentbus discover                     # scan tmux panes and auto-register agents in project dir
agentbus add-agent <name>             # add custom agent type to agents.json
```

---

## Runtime Files

All runtime state lives in `~/.agentbus/`:

```
~/.agentbus/
├── config.json       # global user config (multiplexer preference, etc.)
├── agentbus.db       # SQLite DB — brokers, agents, messages
├── agents.json       # agent type definitions (prompt_pattern, response_wait)
└── logs/
    ├── project-A.log # broker log for project-A
    └── project-B.log # broker log for project-B
```

---

## Key Decisions

- Broker runs on a dynamic port (starting at 7373); port written to `~/.agentbus/port`
- `agentbus start` always runs auto-discovery; use `--no-discover` to skip
- Watchers auto-reconnect on broker restart (retry every 2s, up to 30s)
- `agentbus inbox` returns immediately by default; `--wait` blocks for scripted use
- Message responses hard-truncated at 32KB; agents should pass file paths for large content
- Instance names are never reused within a broker session
- Natural language skill is distributed separately from the binary
