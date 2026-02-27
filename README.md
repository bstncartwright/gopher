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

run a gopher server on exe.dev and chat with it via telegram.

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
- telegram executive channel + panel control-pane hardening.

## core concepts

### session

a long-lived conversational workspace. one conversation thread → one session. contains events, participants, and state. durable across restarts. primary unit of execution.

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

the central process that owns session state, persists events, schedules execution, can run agents locally, and connects external interfaces (telegram, panel, APIs, etc.). the gateway is authoritative but restartable.

### node (optional)

a worker process that can execute agents or tools remotely. nodes are stateless, disposable, capability-advertising, and connected via nats. not required for MVP.

## architecture

```
Telegram DM
     ↓
Gopher Gateway
     ↓
Session Runtime
     ↓
LLM Provider
     ↓
Response → Telegram DM
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

**prerequisites:** go 1.24+, nats (for gateway/node distributed mode)

```bash
# build
go build -o gopher ./cmd/gopher

# init config (optional)
gopher gateway config init
gopher node config init

# run gateway
gopher gateway run --node-id gateway --nats-url nats://127.0.0.1:4222

# run worker node
gopher node run --node-id node-1 --nats-url nats://127.0.0.1:4222

# configure/restart an existing remote node from gateway side
gopher node configure --target-node node-1 --node-heartbeat-interval 5s --capability tool:gpu
gopher node restart --target-node node-1

# help
gopher help
gopher gateway run --help
gopher node run --help
gopher onboard --help
gopher reset --yes
```

gateway config is loaded from `gopher.toml` in the working directory (`gopher.local.toml` overrides). node config is loaded from `node.toml` (`node.local.toml` overrides). env vars `GOPHER_*` override config.

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

install a worker node (includes `/etc/gopher/node.toml` + `gopher-node.service`):

```bash
GOPHER_GITHUB_TOKEN=<token> ./scripts/install.sh --role node
```

service checks:

```bash
gopher status
gopher logs --unit gopher-node.service --lines 200
```

## auth config cli

`gopher auth` provides provider-aware auth key management for env files.
default path resolution is:
- `$GOPHER_ENV_FILE` (if set)
- `~/.gopher/gopher.env` (non-root)
- `/etc/gopher/gopher.env` (root)

for systemd service-managed installs, pass the system path explicitly:

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

web search MCP auth (Exa primary, Tavily fallback) is configured as raw env keys:

```bash
gopher auth set --env-file /etc/gopher/gopher.env --key EXA_API_KEY --value "<exa_api_key>"
gopher auth set --env-file /etc/gopher/gopher.env --key TAVILY_API_KEY --value "<tavily_api_key>"
```

to block specific web MCP hosts, use `policies.network.block_domains` (denylist), for example:
- `mcp.exa.ai`
- `mcp.tavily.com`

## setup + reset

`gopher onboard` bootstraps local defaults and lets you configure auth + telegram env values.

```bash
# interactive
gopher onboard

# non-interactive
gopher onboard \
  --non-interactive \
  --auth-provider zai \
  --auth-api-key "$ZAI_API_KEY" \
  --telegram-bot-token "$GOPHER_TELEGRAM_BOT_TOKEN" \
  --telegram-chat-id "$GOPHER_TELEGRAM_CHAT_ID"
```

`gopher reset` removes config and runtime memory/state while preserving the auth env file.

```bash
gopher reset --yes
```

## agent registry cli

`gopher agent` manages local agent identities and workspaces:

```bash
# create agent registry entry + scaffold workspace files
gopher agent create --id planner --user-id tg:planner

# list agents and statuses
gopher agent list

# soft delete (status only)
gopher agent delete --id planner

# hard delete (status + workspace directory removal)
gopher agent delete --id planner --hard
```

## builder + opencode (manual config)

this is an opt-in, per-agent setup. gopher does not scaffold a special builder preset.

prerequisites:
- `opencode` installed on the host and available on `PATH` (`opencode --help`)
- credentials already configured outside gopher (`opencode auth list`, `opencode auth login`)
- runtime tools enabled in the agent config (`group:runtime`)

example `agents/builder/config.json`:

```json
{
  "agent_id": "builder",
  "name": "builder",
  "role": "assistant",
  "model_policy": "zai:glm-5",
  "enabled_tools": ["group:fs", "group:runtime", "group:collaboration"],
  "max_context_messages": 40
}
```

example `agents/builder/policies.json`:

```json
{
  "fs_roots": ["./"],
  "allow_cross_agent_fs": false,
  "can_shell": true,
  "shell_allowlist": ["echo", "git", "go", "bun", "node", "bash", "gopher", "opencode"],
  "network": {
    "enabled": true,
    "block_domains": []
  },
  "budget": {
    "max_tokens_per_session": 200000
  }
}
```

recommended command pattern from the agent:

```bash
opencode run --format json "<task>"
```

runtime guardrails for `opencode` via `exec`:
- requires `run` subcommand
- requires `--format json`
- blocks interactive flags (`--continue`, `--session`, `--fork`, `--attach`)
- blocks `--dir` (use `exec` tool `workdir` instead)
- blocks `background=true` for `opencode`
- returns actionable preflight error when `opencode` is not found on `PATH`

troubleshooting:
- `exec denied: command "opencode" is not in shell_allowlist`:
  add `opencode` to `shell_allowlist`.
- `exec denied: opencode binary not found in PATH`:
  install/fix `opencode` path on the runtime host.
- `exec denied: opencode one-shot automation requires opencode run --format json ...`:
  rewrite command to the standardized one-shot format.
- non-zero `opencode` exits now include `opencode_troubleshooting` hints in tool results.

## telegram single-agent dm setup

1. prepare runtime workspace(s):
   - `<working_dir>/agents/USER.md` (shared user profile)
   - `<working_dir>/agents/<agent_id>/AGENTS.md`
   - `<working_dir>/agents/<agent_id>/SOUL.md`
   - `<working_dir>/agents/<agent_id>/TOOLS.md`
   - `<working_dir>/agents/<agent_id>/IDENTITY.md`
   - `<working_dir>/agents/<agent_id>/config.json`
   - `<working_dir>/agents/<agent_id>/policies.json`
2. configure gateway telegram block in `/etc/gopher/gopher.toml`:

```toml
[gateway.telegram]
enabled = true
mode = "polling" # default; set to "webhook" for tailscale funnel ingress
bot_token = "<telegram_bot_token>"
poll_interval = "2s"
poll_timeout = "30s"
allowed_user_id = "<telegram_user_id>"
allowed_chat_id = "<telegram_chat_id>"

[gateway.telegram.webhook]
listen_addr = "127.0.0.1:29330"
path = "/_gopher/telegram/webhook"
url = "https://<funnel-hostname>/_gopher/telegram/webhook"
secret = "<telegram_webhook_secret>"
```

webhook mode notes:
- keep `listen_addr` on loopback and expose it with tailscale funnel.
- `url` must be the public `https://` endpoint telegram can reach.
- gopher auto-manages telegram webhook registration:
  - `mode = "webhook"` -> calls `setWebhook`
  - `mode = "polling"` -> calls `deleteWebhook`

example tailscale funnel command:

```bash
tailscale funnel --bg 127.0.0.1:29330
```

3. configure model provider key(s) in `/etc/gopher/gopher.env` and restart:
   - `sudo systemctl restart gopher-gateway.service`

dm control commands:
- `!context clear` / `!context reset`: rotate to a fresh session for the dm.
- `!context summarize` / `!context summary`: request a short in-session summary.
- `!trace` / `!trace link`: ensure trace is on and return the trace conversation id.
- `!trace on|off|status`: toggle/query trace publishing for the dm.

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

optional. when enabled, nodes advertise capabilities, the gateway schedules work across nodes, and agents can run remotely. the gateway remains authoritative. if nodes disappear, sessions requiring unavailable capabilities fail with an explicit scheduler error; agents without required capabilities continue locally.

gateway automatically forwards provider auth env vars to the selected worker node for each remote execution request, so you do not need to configure model auth on every node. supported keys by default:

- `OPENAI_API_KEY`
- `ZAI_API_KEY`
- `EXA_API_KEY`
- `TAVILY_API_KEY`
- `KIMI_API_KEY`
- `ANTHROPIC_API_KEY`
- `OLLAMA_API_KEY`
- `OPENAI_CODEX_API_KEY`
- `OPENAI_CODEX_TOKEN`
- `OPENAI_CODEX_REFRESH_TOKEN`
- `OPENAI_CODEX_TOKEN_EXPIRES`

optional controls:
- disable forwarding: `GOPHER_GATEWAY_SHARE_AUTH_ENV=false`
- override forwarded key list: `GOPHER_GATEWAY_SHARED_AUTH_ENV_KEYS=OPENAI_API_KEY,ZAI_API_KEY,EXA_API_KEY,TAVILY_API_KEY`

node admin controls over NATS:
- `gopher node configure --target-node <id> ...` persists remote `node.toml` and can request restart.
- `gopher node restart --target-node <id>` requests remote restart with best-effort rejoin warning.
- agents can invoke these via the `exec` tool when `gopher` is allowed in `shell_allowlist`.

## safety philosophy

gopher prioritizes durability, observability, simplicity, and personal control over multi-tenant isolation, enterprise security models, and automatic autonomy.

## roadmap

- **phase 1 — session runtime** — durable, event-driven sessions ✅
- **phase 2 — distributed execution** — optional nodes via nats ✅
- **phase 3 — memory system** — persistent knowledge across sessions ✅
- **current — wire gateway executor + telegram interface** — chat with gopher via telegram
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
