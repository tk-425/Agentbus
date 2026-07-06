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

### `agentbus reply <request-id> "<message>"`
Answer a **request** received from another agent. Run this once the work is done;
the injected request instruction names the exact command with the `<request-id>`
already filled in.

- `<request-id>` — the ID from the injected request instruction; copy it verbatim.
- The message is the final positional argument; quote it. Put only the answer —
  no reasoning, no restating the question.
- Takes no `--from`/`--to`: the broker resolves the original requester from the
  request ID and routes the reply back to its inbox as a terminal reply.
- Errors loudly if the request ID is unknown (e.g. already answered) — a reply is
  produced at most once per request.

### `agentbus inbox --name <me>`
Read and clear this agent's inbox (drain-on-read — messages are marked read and
will not appear again). Prints each queued reply as `[reply] from <sender>: <body>`.

- `--wait` — block until at least one message arrives instead of returning empty.
  For **non-interactive scripts only** — an interactive agent gets the injected
  `[agentbus]` notification, so it should end its turn rather than block here.
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
- When answering a received request, run `agentbus reply <request-id> "<answer>"`
  with the ID from the injected instruction. The broker routes the answer back to
  the original requester's inbox as the terminal reply. If the reply command is
  never run, no reply is produced and the requester can re-ask.

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
