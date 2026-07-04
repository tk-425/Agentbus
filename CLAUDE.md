# agentbus

**Version:** v0.4.5

A local multi-agent message bus that lets any running AI coding agent send and receive
messages from any other running agent in real time, via tmux or herdr and a local broker.

---

## Tech Stack

- **Language:** Go
- **CLI framework:** cobra (`github.com/spf13/cobra`)
- **Multiplexer backends:** tmux and herdr (selected by runtime environment — tmux inside a tmux pane, herdr inside a herdr pane; errors when run outside both)
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
agentbus whoami                       # print the current pane's registered instance name
agentbus send --from <agent> --to <agent> <message>  # send message (replies route back to --from)
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
- `agentbus start` runs continuous auto-discovery (immediately on startup, then reconciling on an interval)
- `agentbus start` is idempotent: a second start in the same project reports the live broker instead of launching another
- Watchers auto-reconnect on broker restart (retry every 2s, up to 30s)
- `agentbus inbox` returns immediately by default; `--wait` blocks for scripted use
- Reply arrival is announced by injecting a one-time notification into the requester's pane when idle; reply bodies are never injected — agents read them via `agentbus inbox`
- Message responses hard-truncated at 32KB; agents should pass file paths for large content
- Instance names are never reused within a broker session
- Natural language skill is distributed separately from the binary
