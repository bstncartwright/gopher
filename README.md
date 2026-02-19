# gopher

a lightweight, durable, distributed agent runtime for personal infrastructure. inspired by [open-claw](https://github.com/open-claw/open-claw) and [mini-claw](https://github.com/open-claw/mini-claw), but specialized to my needs and written in go. hence the name.

## philosophy

- **open source.** people can contribute, fork, and use it.
- **user-driven.** i'm driving what i need, not what the community needs. there are already community solutions; this one serves my workflow.
- **personal.** if a feature helps me, it ships. if it doesn't, it doesn't. no roadmap theater.

## why gopher exists

most agent frameworks assume:

- stateless request/response interactions
- cloud-native infrastructure
- heavy orchestration layers
- short-lived tasks

gopher instead targets:

- persistent agents
- long-running sessions
- personal deployment
- self-hosted environments
- incremental complexity

you can start with one server and grow into a distributed system without redesign.

## mvp goal

run a gopher server on exe.dev and chat with it via matrix.

## current status (code as source of truth)

**working:**

- `gopher gateway run` – starts the gateway node, connects to nats, publishes capabilities, accepts control messages
- `gopher gateway config init` – writes starter gopher.toml
- full agentcore library – run turns, tools (read, write, edit, apply_patch, shell, process, cron), memory, context assembly, loop detection
- session runtime – event persistence, replay, jsonl logging
- ai providers – openai completions/responses, anthropic messages, ollama, openai-codex, kimi-coding, zai
- memory system – sqlite store, retriever, embedder, ingestion (extractor, publisher), scoped storage
- distributed execution plumbing – nats fabric, node runtime with heartbeats, scheduler, capability-based routing

**in progress:**

- gateway executor is a stub – returns "gateway executor is not configured yet" on execution requests. wiring it to a real agent is next.
- matrix multi-agent routing – current bridge is single-agent DM first. task-room fanout and per-agent identity registry are phase 2.

## core concepts

### session

a long-lived conversational workspace. one matrix room → one session (MVP mapping, when matrix lands). contains events, participants, and state. durable across restarts. primary unit of execution.

### event

an immutable record of something that happened. examples: user message, agent response, tool call, system control signal. the event log is the source of truth.

### agent

an entity that processes context and produces actions. agents can run locally on the gateway or remotely on nodes (optional), with access to memory and tools.

### memory

persistent knowledge derived from sessions. memory is scoped, not owned by a single component.

| scope | purpose |
|-------|---------|
| session | short-term context |
| agent | identity and learned behavior |
| project | shared domain knowledge |
| global | system-wide facts |

### gateway

the central process that owns session state, persists events, schedules execution, can run agents locally, and connects external interfaces (matrix, APIs, etc.). the gateway is authoritative but restartable.

### node (optional)

a worker process that can execute agents or tools remotely. nodes are stateless, disposable, capability-advertising, and connected via nats. not required for MVP.

## architecture

```
Matrix Client
     ↓
Matrix Homeserver (Conduit)  [planned]
     ↓
Gopher Gateway
     ↓
Session Runtime
     ↓
LLM Provider
     ↓
Response → Matrix Room
```

with distributed execution enabled:

```
Gateway ── NATS ── Nodes
```

## project structure

```
cmd/gopher/          # CLI entry point
  main.go            # root dispatcher
  gateway_run.go     # gateway run, config init
  temp_executor.go   # stub executor (to be replaced)

pkg/
  agentcore/         # agent loop, tools, run_turn, memory integration
  ai/                # providers, models, streaming, oauth
  config/            # gopher.toml loading
  context/           # assembler, token budget, memory section
  fabric/nats/       # nats client, bus, subjects
  gateway/           # node sync, event publisher, distributed executor
  memory/            # store, retriever, embedder, ingestion pipeline
  node/              # node runtime, heartbeats, control handling
  scheduler/         # registry, capability-based selection
  session/           # event sourcing, persistence, replay
  store/sqlite/      # event store
```

## running

**prerequisites:** go 1.24+, nats (for gateway mode)

```bash
# build
go build -o gopher ./cmd/gopher

# init config (optional)
gopher gateway config init

# run gateway
gopher gateway run --node-id gateway --nats-url nats://127.0.0.1:4222

# help
gopher help
gopher gateway run --help
```

config can be loaded from `gopher.toml` in the working directory; `gopher.local.toml` overrides. env vars `GOPHER_*` override config.

## install on linux vm

for private-release installs, use the bootstrap script:

```bash
GOPHER_GITHUB_TOKEN=<token> ./scripts/install.sh --role gateway --with-nats
```

what it does:
- downloads latest release asset for your linux arch
- verifies checksums
- installs binary to `/usr/local/bin/gopher`
- initializes `/etc/gopher/gopher.toml` (if missing)
- creates `/etc/gopher/gopher.env` (if missing)
- installs/starts `gopher-gateway.service`

use a specific release:

```bash
GOPHER_GITHUB_TOKEN=<token> ./scripts/install.sh --version v0.1.0
```

install a worker node (binary only, no local service/nats):

```bash
GOPHER_GITHUB_TOKEN=<token> ./scripts/install.sh --role node
```

## auth config cli

`gopher auth` provides provider-aware auth key management for service env files:

```bash
# list provider auth status (configured/missing)
gopher auth list --env-file /etc/gopher/gopher.env

# list supported providers and env keys
gopher auth providers

# set provider key
gopher auth set --env-file /etc/gopher/gopher.env --provider zai --api-key "$ZAI_API_KEY"

# run interactive oauth login for openai-codex
gopher auth login --env-file /etc/gopher/gopher.env --provider openai-codex

# remove provider key
gopher auth unset --env-file /etc/gopher/gopher.env --provider zai
```

openai-codex oauth login stores:
- `OPENAI_CODEX_TOKEN`
- `OPENAI_CODEX_REFRESH_TOKEN`
- `OPENAI_CODEX_TOKEN_EXPIRES` (unix ms)

runtime uses these oauth fields to refresh `OPENAI_CODEX_TOKEN` automatically when expired.

manual fallback is still supported:

```bash
gopher auth set --env-file /etc/gopher/gopher.env --key OPENAI_CODEX_TOKEN --value "<token>"
```

## agent registry cli

`gopher agent` manages local agent identities and workspaces:

```bash
# create agent registry entry + scaffold workspace files
gopher agent create --id planner --matrix-user @planner:example.com

# list agents and statuses
gopher agent list

# soft delete (status only)
gopher agent delete --id planner

# hard delete (status + workspace directory removal)
gopher agent delete --id planner --hard
```

## matrix single-agent dm setup (conduit)

phase-1 objective: one matrix bot user (`bot_user_id`) that accepts dm messages and routes them through one runtime agent workspace.

1. prepare runtime workspace(s):
   - preferred layout (isolated per agent):
     - `<working_dir>/agents/<agent_id>/AGENTS.md`
     - `<working_dir>/agents/<agent_id>/SOUL.md`
     - `<working_dir>/agents/<agent_id>/TOOLS.md`
     - `<working_dir>/agents/<agent_id>/IDENTITY.md`
     - `<working_dir>/agents/<agent_id>/USER.md`
     - `<working_dir>/agents/<agent_id>/HEARTBEAT.md` (optional)
     - `<working_dir>/agents/<agent_id>/BOOTSTRAP.md` (brand-new workspaces)
     - `<working_dir>/agents/<agent_id>/MEMORY.md` (optional)
     - `<working_dir>/agents/<agent_id>/config.json`
     - `<working_dir>/agents/<agent_id>/policies.json`
   - compatibility:
     - runtime reads canonical uppercase files first
     - if missing, runtime falls back to lowercase legacy names (`soul.md`, `tools.md`, etc.)
   - optional skills layout (agent-skills compatible):
     - `<working_dir>/agents/<agent_id>/.agents/skills/<skill_name>/SKILL.md`
     - frontmatter must include:
       - `name`
       - `description`
     - example:

```markdown
---
name: fixing-accessibility
description: Fix accessibility issues in UI code.
---
## workflow

Run the accessibility audit and apply fixes.
```

   - discovery order:
     - `config.json` `skills_paths` (if set)
     - `AGENT_SKILLS_PATH` (path-list separated)
     - defaults: `<workspace>/.agents/skills` and `~/.agents/skills`
   - runtime behavior:
     - startup loads skill metadata (`name`, `description`, `location`)
     - system prompt uses an OpenClaw-style sectioned layout (`full|minimal|none`; default `full`)
     - `<available_skills>` metadata is injected; full skill instructions are loaded on demand via `read`
     - explicit skill invocation is supported via `/skill:<name> [args]`
     - bootstrap files are injected every turn with caps:
       - `bootstrap_max_chars` (default `20000`) per file
       - `bootstrap_total_max_chars` (default `150000`) total across injected files
     - memory model is hybrid:
       - `MEMORY.md` / `memory.md` can be injected as bootstrap context
       - JSON working memory remains injected as a gopher extension
     - long-term memory retrieval is injected only for `full` prompt mode
     - heartbeat polling is opt-in per agent via `config.json`:
       - `heartbeat.every` (duration; required to enable, examples: `"15m"`, `"1h"`, `"30"` where bare numbers mean minutes)
       - `heartbeat.prompt` (optional custom poll prompt)
       - `heartbeat.ack_max_chars` (optional suppression threshold for `HEARTBEAT_OK` replies; default `300`)
       - heartbeat dispatch targets the scheduled agent explicitly via `target_actor_id` (no `@mention` required)
       - in matrix room=session flows with multiple agents, heartbeat is skipped when the target agent's managed user is not currently joined in that room
   - cross-agent file access is policy-gated in `policies.json`:
     - set `allow_cross_agent_fs = true` and include additional paths in `fs_roots` when an agent should read/write another agent workspace
2. configure gateway matrix block in `/etc/gopher/gopher.toml`:
   - `enabled = true`
   - `homeserver_url = "http://127.0.0.1:6167"` (or your matrix base url)
   - `appservice_id`, `as_token`, `hs_token`, `listen_addr`, `bot_user_id`
   - `bot_user_id` is used as a domain template. runtime maps each `agent_id` to `@<agent_id>:<domain>`.
     example: if `bot_user_id = "@gopher:gophers.bostonc.dev"` and `agent_id = "gateway-agent"`,
     the runtime matrix user is `@gateway-agent:gophers.bostonc.dev`.
   - `presence_enabled = true` (default)
   - `presence_interval = "60s"` (default keepalive)
   - `presence_status_msg = ""` (optional custom status)
   - `rich_text_enabled = true` (default; renders markdown replies as sanitized html with plain-text fallback)
3. configure model provider key in `/etc/gopher/gopher.env` (example: `ZAI_API_KEY=...`) and restart:
   - `sudo systemctl restart gopher-gateway.service`
4. register appservice in conduit admin room using `/etc/gopher/gopher-appservice-registration.yaml`
   - command message: `@conduit:<server_name>: register-appservice`
   - then paste yaml payload
   - verify with: `@conduit:<server_name>: list-appservices`
5. run smoke test:

```bash
python3 scripts/matrix_dm_smoke.py \
  --homeserver http://127.0.0.1:6167 \
  --registration-token <matrix_registration_token> \
  --bot-user-id @gopher:<server_name>
```

expected result:
- `bot_membership=join`
- `bot_reply_count>=1`

to disable rich matrix formatting for compatibility debugging:
- toml: set `[gateway.matrix] rich_text_enabled = false`
- env: `GOPHER_GATEWAY_MATRIX_RICH_TEXT_ENABLED=false`
- cli: `--matrix-rich-text-enabled=false`

## releases

gopher now includes a github actions release workflow at `.github/workflows/release.yml`.

- trigger: push a tag like `v0.1.0` (or run manual dispatch with a `v*` tag)
- build outputs:
  - `gopher-linux-amd64`
  - `gopher-linux-arm64`
  - `checksums.txt`
- release publishing: github release is created automatically with those assets

example:

```bash
git tag v0.1.0
git push origin v0.1.0
```

## durability

gopher persists sessions, event history, and memory records. on restart, the gateway reloads sessions, replays events, reconstructs state, and resumes. no manual recovery required.

## cron jobs (gateway)

the gateway can run durable cron jobs that inject a user-role message into a target session, which then triggers a normal agent turn.

- creation path in v1: agent tool (`cron`) only
- persistence path: `.gopher/cron/jobs.json`
- restart behavior: one-time catchup (if a run was missed while offline, run once immediately, then continue the schedule)
- config block:

```toml
[gateway.cron]
enabled = true
poll_interval = "1s"
default_timezone = "UTC"
```

env overrides:
- `GOPHER_GATEWAY_CRON_ENABLED`
- `GOPHER_GATEWAY_CRON_POLL_INTERVAL`
- `GOPHER_GATEWAY_CRON_DEFAULT_TIMEZONE`

## distributed execution (phase 2)

optional. when enabled, nodes advertise capabilities, the gateway schedules work across nodes, and agents can run remotely. the gateway remains authoritative. if nodes disappear, the system continues locally. the plumbing exists; the gateway executor is not yet wired to a real agent.

## safety philosophy

gopher prioritizes durability, observability, simplicity, and personal control over multi-tenant isolation, enterprise security models, and automatic autonomy.

## roadmap

- **phase 1 — session runtime** — durable, event-driven sessions ✅
- **phase 2 — distributed execution** — optional nodes via nats ✅
- **phase 3 — memory system** — persistent knowledge across sessions ✅
- **current — wire gateway executor + matrix interface** — chat with gopher via matrix
- **future** — improved agent loops, expanded tools, agent identities, automation, multi-gateway HA

## design principles

- incrementally adoptable
- self-host friendly
- durable by default
- transparent to debug
- useful before complete
- simplicity over cleverness
- explicit behavior over magic

## contributing

contributions are welcome. the roadmap is driven by what i need. if you open an issue or PR, expect that it might not align with my priorities. forks are encouraged if you want to take it in a different direction.
