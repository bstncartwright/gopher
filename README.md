# gopher

gopher is a lightweight, durable, distributed agent runtime for personal infrastructure.

It is inspired by [open-claw](https://github.com/open-claw/open-claw) and [mini-claw](https://github.com/open-claw/mini-claw), but built around local workspaces, long-lived sessions, self-hosted deployment, and a Go runtime that can start simple and grow into a small distributed system.

## what exists today

- a gateway runtime that loads one or more local agent workspaces and runs durable sessions
- telegram dm transport in polling or webhook mode
- a loopback web panel for sessions, cron, nodes, agents, and remotes
- built-in agent tools for filesystem work, shell/process execution, memory, web search/fetch, delegation, heartbeat, messaging, reactions, and cron
- persistent session state, replay, and jsonl event logs under a workspace-local `.gopher/`
- local memory indexing, retrieval, and diagnostics
- optional worker nodes over nats with capability-based routing and remote node admin
- linux install, service, onboarding, auth, update, and reset tooling

current focus:

- hardening the telegram/control-plane workflow
- iterating on agent templates, delegation, and remote runtime integrations

## why gopher exists

most agent frameworks assume:

- stateless request/response interactions
- cloud-native infrastructure
- heavy orchestration layers
- short-lived tasks

gopher instead targets:

- persistent agents
- long-running sessions
- local or self-hosted deployment
- explicit, inspectable state
- incremental complexity

you can start with one gateway on one machine and add worker nodes later without changing the basic model.

## architecture

single-node flow:

```text
Telegram / local control
          |
          v
     Gopher Gateway
          |
          v
     Session Runtime
          |
          v
      Agent Runtime
          |
          v
     LLM + Tools + Memory
```

with distributed execution enabled:

```text
Gateway -- NATS -- Worker Nodes
```

the gateway remains authoritative for sessions and scheduling. nodes advertise capabilities and execute work remotely when selected.

## workspace model

the runtime workspace is the directory that contains `gopher.toml` (or `node.toml`), or the current working directory if you run without a config file.

gopher expects agent workspaces under `agents/` inside that runtime workspace:

```text
<workspace>/
  gopher.toml
  gopher.local.toml
  node.toml
  node.local.toml
  agents/
    USER.md
    main/
      config.toml
      AGENTS.md
      SOUL.md
      TOOLS.md
      IDENTITY.md
      USER.md
      HEARTBEAT.md
      BOOTSTRAP.md
      memory/
      logs/
    <other-agent>/
      config.toml
      ...
  .gopher/
    sessions/
    cron/
    ...
```

notes:

- if no agent workspace exists, `gopher gateway run` creates `agents/main` automatically
- `config.toml` is the default agent config format; `config.json` is also accepted for agent workspaces
- gateway runtime state lives under `<workspace>/.gopher`
- the default scaffolded agent model policy is `openai-codex:gpt-5.4`

## prerequisites

- go `1.26+` to build from source
- a reachable nats server for `gopher gateway run` and `gopher node run`
- provider credentials in the process environment or a service env file
- a telegram bot token only if you want telegram integration

## build

`just build` is the normal build path:

```bash
just build
```

that regenerates `pkg/ai/models_generated.go` from `https://models.dev/api.json` and builds `./gopher`.

if you want a quick local build without regenerating the checked-in model catalog:

```bash
go build -o gopher ./cmd/gopher
```

useful development commands:

```bash
just fmt
just lint
go test ./...
```

## quick start

local foreground run:

```bash
mkdir -p ~/gopher-dev
cd ~/gopher-dev

# write starter gateway config in the current workspace
gopher gateway config init

# scaffold the main agent workspace up front
gopher agent create --id main --workspace-root ./agents

# put provider creds in the default env file
gopher auth login --provider openai-codex

# direct foreground runs do not auto-load ~/.gopher/gopher.env
set -a
. ~/.gopher/gopher.env
set +a

# start nats, then start the gateway
nats-server -js
gopher gateway run --config ./gopher.toml
```

after the gateway starts:

- the panel is available on `http://127.0.0.1:29329` by default
- `agents/main/config.toml` is the place to change model/provider/runtime settings
- additional local agents live under `agents/<id>/`

if you prefer service-managed installs, use `gopher service install` or `./scripts/install.sh` instead of running in the foreground.

## cli overview

top-level commands:

```text
gopher gateway run
gopher gateway config init
gopher node run
gopher node configure
gopher node restart
gopher node config init
gopher status
gopher restart
gopher logs
gopher service ...
gopher agent ...
gopher auth ...
gopher onboard
gopher pair ...
gopher memory ...
gopher doctor memory
gopher update
gopher reset
gopher version
```

help entry points:

```bash
gopher help
gopher gateway run --help
gopher node run --help
gopher agent --help
gopher service --help
```

## configuration

gateway config loading order:

1. built-in defaults
2. `gopher.toml`
3. `gopher.local.toml`
4. `GOPHER_GATEWAY_*` env vars
5. cli flags

node config loading order:

1. built-in defaults
2. `node.toml`
3. `node.local.toml`
4. `GOPHER_NODE_*` env vars
5. cli flags

useful defaults from the generated starter config:

- gateway panel: `127.0.0.1:29329`
- telegram webhook listener: `127.0.0.1:29330`
- cron enabled: `true`
- cron timezone: `UTC`
- nats url: `nats://127.0.0.1:4222`

starter configs:

```bash
gopher gateway config init
gopher node config init
```

## agent runtime

the gateway loads every agent workspace under `agents/*` that contains `config.toml` or `config.json`.

the scaffolded workspace files are:

- `AGENTS.md`
- `SOUL.md`
- `TOOLS.md`
- `IDENTITY.md`
- `USER.md`
- `HEARTBEAT.md`
- `BOOTSTRAP.md`
- `config.toml`

manage those workspaces with:

```bash
# use the active runtime workspace
gopher agent create --id planner --workspace-root ./agents
gopher agent list --workspace-root ./agents
gopher agent delete --id planner --workspace-root ./agents

# default registry/workspace root is ~/.gopher/agents
gopher agent create --id helper
gopher agent list
```

the default enabled tool groups cover:

- memory search and memory fetch
- file read/write/edit
- shell exec and background process management
- gopher metadata and self-update helpers
- delegation, delegate target discovery, heartbeat, messaging, and reactions
- web search and web fetch
- cron scheduling

experimental runtime option:

- agent workspaces can set `runtime.type = "acp"` and use built-in acp targets `codex` or `opencode`

## auth and provider credentials

`gopher auth` manages provider credentials in an env file.

supported providers:

- `openai`
- `openai-codex`
- `anthropic`
- `kimi-coding`
- `zai`
- `ollama`

default auth env file resolution:

- `$GOPHER_ENV_FILE`, if set
- `~/.gopher/gopher.env` for non-root users
- `/etc/gopher/gopher.env` when running as root

examples:

```bash
gopher auth providers
gopher auth list

gopher auth set --provider zai --api-key "$ZAI_API_KEY"
gopher auth set --provider anthropic --api-key "$ANTHROPIC_API_KEY"

gopher auth login --provider openai-codex
gopher auth unset --provider zai
```

important:

- service installs read the env file automatically via systemd
- direct foreground runs do not; export the variables in your shell or source the env file yourself
- `GOPHER_TELEGRAM_BOT_TOKEN` is also used by onboarding/service flows for telegram setup

web search credentials are stored as raw env keys:

```bash
gopher auth set --key EXA_API_KEY --value "<exa_api_key>"
gopher auth set --key TAVILY_API_KEY --value "<tavily_api_key>"
```

to block specific web mcp hosts, use `policies.network.block_domains` in the agent config.

## onboarding and reset

`gopher onboard` can write default configs and walk through auth + telegram setup:

```bash
# interactive
gopher onboard

# non-interactive
gopher onboard \
  --non-interactive \
  --auth-provider zai \
  --auth-api-key "$ZAI_API_KEY" \
  --telegram-bot-token "$GOPHER_TELEGRAM_BOT_TOKEN"
```

`gopher reset` is destructive. it removes workspace and global gopher state while preserving the selected auth env file:

```bash
gopher reset --yes
```

## telegram

gateway telegram support can run in `polling` or `webhook` mode.

minimal polling config:

```toml
[gateway.telegram]
enabled = true
mode = "polling"
bot_token = "replace-telegram-bot-token"
poll_interval = "2s"
poll_timeout = "30s"
allowed_user_id = ""
allowed_chat_id = ""
```

webhook mode:

```toml
[gateway.telegram]
enabled = true
mode = "webhook"
bot_token = "replace-telegram-bot-token"

[gateway.telegram.webhook]
listen_addr = "127.0.0.1:29330"
path = "/_gopher/telegram/webhook"
url = "https://example.ts.net/_gopher/telegram/webhook"
secret = "replace-webhook-secret"
```

notes:

- webhook mode requires a public `https://` url
- the webhook listener should stay on loopback and be exposed through your ingress layer
- the gateway manages telegram webhook registration based on mode

pairing helpers:

```bash
gopher pair status --workspace ~/.gopher
gopher pair approve --workspace ~/.gopher
```

telegram-facing tools available in dm sessions:

- `message` sends a visible chat message immediately
- `reaction` reacts to the most recent inbound user message

## memory and cron

memory commands operate on an agent workspace:

```bash
gopher memory status --workspace ./agents/main
gopher memory index --workspace ./agents/main
gopher memory index --workspace ./agents/main --force
gopher doctor memory --workspace ./agents/main
```

cron jobs are durable gateway-side scheduled messages that trigger normal agent turns.

relevant gateway config:

```toml
[gateway.cron]
enabled = true
poll_interval = "1s"
default_timezone = "UTC"
```

cron state is stored under `.gopher/cron/`.

## distributed execution

worker nodes are optional, but the runtime is built around a shared nats fabric.

start a node:

```bash
gopher node config init
gopher node run --config ./node.toml
```

configure or restart a remote node from the gateway side:

```bash
gopher node configure --target-node node-1 --node-heartbeat-interval 5s --capability tool:gpu
gopher node restart --target-node node-1
```

current distributed behavior:

- nodes advertise capabilities over nats
- the gateway routes work based on required capabilities
- provider auth env vars can be forwarded from gateway to selected nodes for remote execution
- if a required capability is unavailable, the scheduler fails explicitly instead of silently degrading

## services and linux installs

for local linux service management:

```bash
gopher service install --role gateway
gopher service install --role node

gopher service status
gopher service restart --role gateway
gopher service logs --role node --lines 200
```

service defaults:

- service working/state directory: `~/.gopher`
- gateway config path: `~/.gopher/gopher.toml`
- node config path: `~/.gopher/node.toml`
- env file: `~/.gopher/gopher.env`

when installed as a service:

- `gopher-gateway.service` runs `gopher gateway run --config <path>`
- `gopher-node.service` runs `gopher node run --config <path>`

the bootstrap installer is still available for linux release installs:

```bash
GOPHER_GITHUB_TOKEN=<token> ./scripts/install.sh --role gateway --with-nats
GOPHER_GITHUB_TOKEN=<token> ./scripts/install.sh --role node
```

the installer:

- downloads the correct linux release asset for the current arch
- verifies checksums
- installs the binary
- initializes config/env files when missing
- installs and starts the relevant systemd unit

## updates

binary update command:

```bash
gopher update --check --github-token "$GOPHER_GITHUB_TOKEN"
gopher update --github-token "$GOPHER_GITHUB_TOKEN"
```

notes:

- `gopher update` expects a release-versioned binary, not a `dev` build
- it can restart the inferred service unless `--no-service-restart` is used
- the update token can come from `--github-token`, `GOPHER_GITHUB_TOKEN`, or `GOPHER_GITHUB_UPDATE_TOKEN`

service-aware update commands:

```bash
gopher service update check --config ~/.gopher/gopher.toml --github-token "$GOPHER_GITHUB_TOKEN"
gopher service update apply --config ~/.gopher/gopher.toml --github-token "$GOPHER_GITHUB_TOKEN"
```

those use the gateway's `[gateway.update]` config block to resolve the release source and asset pattern.

## design principles

- incrementally adoptable
- durable by default
- inspectable and easy to debug
- self-host friendly
- explicit behavior over magic
- useful before complete
- simplicity over orchestration theater

## contributing

contributions are welcome, but the project is still driven by personal use cases first. if you open an issue or pr, expect the repo to optimize for practical workflow value over roadmap symmetry.
