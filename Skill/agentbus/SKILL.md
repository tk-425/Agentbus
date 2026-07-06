---
name: agentbus
description: This skill should be used when an AI coding agent needs to communicate or coordinate with other AI coding agents running on the same machine — sending a question or task to a specific other agent, replying to a request it received, receiving a reply, or acting on an "[agentbus] new reply" notification that appears in its pane. Covers the agentbus CLI (whoami, list, send, reply, inbox) and the request/reply conventions. Trigger phrases include "ask the codex agent to…", "have claude-3 check…", "coordinate with the other agent", or seeing an "[agentbus]" line appear unprompted.
---

# agentbus

agentbus is a local message bus that lets running AI coding agents hand work to
each other and receive results. Each agent is a registered pane with an instance
name such as `claude-2` or `codex-1`. Messages are either **requests** (injected
into the recipient's pane and acted on) or **replies** (the recipient's answer,
which lands in the requester's inbox and is read back — never injected).

Use this skill to send a request to another agent, read replies, and answer
requests received from other agents.

## Know your own name first

Every `send` needs a `--from` value: this agent's own instance name. Do not
guess it — resolve it:

```bash
agentbus whoami        # prints this pane's registered name, e.g. claude-2
```

Reuse that value as `--from` for the rest of the session. If `whoami` reports
the pane is not registered, agentbus has not discovered this agent yet — surface
that to the human (the broker may not be running) rather than inventing a name.

## Send a request to another agent

Address the recipient by the exact token `list` prints. How you find it depends
on how the human framed the target:

1. **The human named a project** ("ask opencode in Project-1 to…"): the recipient
   is in another project, so list across every project and use the fully
   qualified `name@project` token:
   ```bash
   agentbus list --all        # every project, one name@project per line
   ```
   Find the matching row (e.g. `opencode-1@Project-1`) and address it exactly:
   ```bash
   agentbus send --from claude-2 --to opencode-1@Project-1 "Run the unit tests in ./api and report only pass/fail plus any failing test names."
   ```
2. **The human named no project** ("ask opencode to…"): the recipient is in this
   project. List the current project (the default scope) and use the bare name:
   ```bash
   agentbus list              # this project only, one name@project per line
   ```
   ```bash
   agentbus send --from claude-2 --to opencode-1 "Run the unit tests in ./api and report only pass/fail plus any failing test names."
   ```
   If more than one instance matches (e.g. two `opencode-*`), do **not** guess —
   ask the human which one to use.

`--from` is always this agent's own name (from `whoami`); `--to` is the recipient;
the message is one self-contained instruction. A bare name resolves within this
project; use `name@project` to reach another project — a bare name that collides
across projects silently resolves to just one of them, so qualify it when the
project matters.

Do not block waiting after sending, and do not poll the inbox. After sending,
tell the human the request is out and then **stop — end your turn**. The reply
routes back automatically and arrives as an injected `[agentbus]` notification
(see next section) that re-engages you when it lands; you do not need to stay
active for it. Large content does not belong in a message — bodies are truncated
at 32KB. Pass a file path and let the recipient read the file.

## Receive a reply

When a reply arrives and this agent is idle, agentbus injects **one** line into
this pane:

```
[agentbus] new reply from codex-1 — run: agentbus inbox --name claude-2
```

Treat that line as expected input, not noise. Act on it by running the command
it names:

```bash
agentbus inbox --name claude-2     # prints queued replies, marks them read
```

The reply *body* is never injected into the pane — reading the inbox is the only
way to see it. Only one notification fires per reply, so read the inbox when
prompted rather than waiting for a repeat. Do **not** run `agentbus inbox`
speculatively before the notification appears: it stays empty until the reply
lands, and looping it just burns turns. Wait for the `[agentbus]` line, then read
once.

`--wait` blocks until a reply arrives. It exists for **non-interactive scripts**
(e.g. a shell pipeline) that have no way to react to the injected notification.
As an interactive agent you *do* get that notification, so do not use `--wait` to
sit and block — end your turn and let the notification re-engage you.

```bash
agentbus inbox --name claude-2 --wait --timeout 120s   # scripts only, not agents
```

## Answer a request received from another agent

A request from another agent arrives as ordinary text injected into this pane,
followed by an instruction like:

> [agentbus: when done, run: agentbus reply abc123 "<your answer>"]

Do the work, then run that command with the request ID it names and your answer
as the message:

```bash
agentbus reply abc123 "All 42 tests passed."
```

The broker matches the request ID to the original requester and routes your
answer to its inbox as the terminal reply. Put only the answer in the message —
no reasoning, no restating the question. If you never run the command, no reply
is produced; the requester can re-ask. Answer a given request ID once — a second
`reply` for the same ID errors because the reply was already delivered.

## Etiquette

- **Delegate only when it helps.** Send to another agent when it has context,
  tools, or a working directory this agent lacks, or when parallel work saves
  real time. Do not offload work that is faster to just do.
- **Address one specific agent.** Pick a target from `agentbus list`; never
  broadcast the same request to many agents.
- **One self-contained instruction per request.** State the task and the exact
  output wanted ("report only pass/fail"). The bus is not a back-and-forth
  chat — avoid asking the requester follow-up questions through it.
- **Send once.** Do not re-send while waiting; the reply notification will
  arrive. Re-sending creates duplicate work and duplicate replies.
- **Pass paths, not payloads.** For anything large, send a file path.
- **Always act on `[agentbus]` notifications.** They mean a reply is waiting;
  read the inbox even if a previous instruction said to stop — reading an inbox
  is terminal and safe.

## Command reference

See `references/api_reference.md` for every command, flag, and exit behavior.
