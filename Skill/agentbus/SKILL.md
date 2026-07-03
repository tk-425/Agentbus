---
name: agentbus
description: This skill should be used when an AI coding agent needs to communicate or coordinate with other AI coding agents running on the same machine — sending a question or task to a specific other agent, receiving the reply, or acting on an "[agentbus] new reply" notification that appears in its pane. Covers the agentbus CLI (whoami, list, send, inbox) and the request/reply conventions. Trigger phrases include "ask the codex agent to…", "have claude-3 check…", "coordinate with the other agent", or seeing an "[agentbus]" line appear unprompted.
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

1. Find valid targets:
   ```bash
   agentbus list        # prints every registered agent as name@project
   ```
2. Send to one specific agent by name. `--from` is this agent (from `whoami`),
   `--to` is the recipient, and the message is one self-contained instruction:
   ```bash
   agentbus send --from claude-2 --to codex-1 "Run the unit tests in ./api and report only pass/fail plus any failing test names."
   ```

Do not block waiting after sending. The reply routes back automatically; a
notification announces it (see next section). Large content does not belong in a
message — bodies are truncated at 32KB. Pass a file path and let the recipient
read the file.

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
prompted rather than waiting for a repeat.

To deliberately block until a reply arrives (e.g. a scripted step that cannot
proceed without it), add `--wait`:

```bash
agentbus inbox --name claude-2 --wait --timeout 120s
```

## Answer a request received from another agent

A request from another agent arrives as ordinary text injected into this pane,
followed by an instruction like:

> …print it wrapped between two lines — one containing only
> `<<AGENTBUS_REPLY abc123>>` and one containing only `<<AGENTBUS_END abc123>>`

Follow that instruction literally. Do the work, then print the final answer
between exactly those two marker lines:

```
<<AGENTBUS_REPLY abc123>>
All 42 tests passed.
<<AGENTBUS_END abc123>>
```

The markers let agentbus extract the actual answer from the terminal frame and
send it back as a clean reply. Skipping them means the requester gets a
diagnostic error instead of an answer. Put only the answer between the markers —
no reasoning, no restating the question. Preserve exact command output whenever
practical; do not summarize, paraphrase, or compress it unless the requester
explicitly asked for a summary or the reply would exceed the bus size limit. If
you cannot fit the full result, return the closest faithful excerpt and clearly
label it as truncated. Everything intended to be returned to the sender must be
printed between the two marker lines. Do not place any part of the reply body
before `<<AGENTBUS_REPLY ...>>` or after `<<AGENTBUS_END ...>>`. Any text
outside the markers is not part of the reply and may cause the sender to
receive an empty or incomplete result.

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
