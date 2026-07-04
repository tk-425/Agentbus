# agentbus command reference

Every command an agent uses to communicate over the bus. Commands that talk to
the bus require a running broker (`agentbus start`, normally started by the
human). All commands print to stdout and return a non-zero exit code on error.

## Identity

### `agentbus whoami`
Print the Agent instance name registered for the current pane (e.g. `claude-2`).
Resolves the pane from `HERDR_PANE` / `TMUX_PANE`. Use its output as `--from`.

- Errors with `this pane is not registered` if the broker has not discovered
  this pane yet. Do not invent a name; tell the human the broker may not be
  running or has not discovered this agent.

### `agentbus list [--all]`
List registered Agent instances, one per line as `name@project`
(e.g. `codex-1@api`). By default it shows only the **current project** — use
these bare names as `--to` targets. Pass `--all` to list every project; use the
fully qualified `name@project` token to target a recipient in another project.

## Sending and receiving

### `agentbus send --from <me> --to <them> "<message>"`
Send a **request** to another agent. Both flags are required.

- `--from` — this agent's instance name (from `whoami`). Replies route back to it.
- `--to` — the recipient, a bare `name` or `name@project`.
- The message is the final positional argument; quote it.
- Bodies are truncated at 32KB — pass a file path for large content.
- Returns immediately. Do not poll; the reply arrives asynchronously.

### `agentbus inbox --name <me>`
Read and clear this agent's inbox (drain-on-read — messages are marked read and
will not appear again). Prints each queued reply as `[reply] from <sender>: <body>`.

- `--wait` — block until at least one message arrives instead of returning empty.
- `--timeout <duration>` — max time `--wait` blocks (default `30s`, e.g. `120s`).

Reply **bodies are only visible through `inbox`** — they are never typed into the
pane. When an `[agentbus] new reply from …` line appears in the pane, run this
command to read the reply it announced.

## Request/reply conventions

- A **request** is injected into the recipient's pane only while it is idle, and
  is expected to produce exactly one reply.
- A **reply** is inbox-only and terminal (it produces nothing further). Its
  arrival is announced once by an injected notification while the requester is
  idle: `[agentbus] new reply from <sender> — run: agentbus inbox --name <you>`.
- When answering a received request, wrap the final answer between the two marker
  lines named in the injected instruction:
  ```
  <<AGENTBUS_REPLY <id>>>
  <answer only>
  <<AGENTBUS_END <id>>>
  ```
  agentbus extracts the text between the markers as the reply body. Without the
  markers the requester receives a diagnostic error instead of the answer.

## Setup commands (usually run by the human, not the agent)

- `agentbus start` — start the broker in the background and auto-discover agents.
- `agentbus stop` — stop the broker.
- `agentbus register --name <type>` — register the current pane as an agent type
  (e.g. `claude`); prints the assigned instance name. Auto-discovery usually
  handles this, so prefer `whoami` to learn an existing name rather than
  re-registering (re-registering can create a second name for the same pane).
- `agentbus discover` — scan panes and auto-register known agent types in the
  current project.
- `agentbus status` — one-line summary of broker/agent/history state.
- `agentbus log` — recent Request/Reply history (durable, independent of inbox).
- `agentbus version` — print the installed agentbus version.
