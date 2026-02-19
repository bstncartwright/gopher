# matrix single-agent ops runbook

this runbook is the repeatable setup path for one matrix-chatable agent on a gateway vm.

## prerequisites

- conduit homeserver reachable from gateway
- `gopher-gateway.service` installed
- provider key available (example: `ZAI_API_KEY`)

## 1) runtime workspace validation

preferred per-agent files under the service working directory (`/home/exedev/.gopher`):

- `agents/<agent_id>/AGENTS.md`
- `agents/<agent_id>/SOUL.md`
- `agents/<agent_id>/TOOLS.md`
- `agents/<agent_id>/IDENTITY.md`
- `agents/<agent_id>/USER.md`
- `agents/<agent_id>/HEARTBEAT.md` (optional; heartbeat checklist)
- `agents/<agent_id>/BOOTSTRAP.md` (brand-new workspaces)
- `agents/<agent_id>/MEMORY.md` (optional)
- `agents/<agent_id>/config.json`
- `agents/<agent_id>/policies.json`

compatibility:
- runtime prefers canonical uppercase files
- if missing, runtime falls back to lowercase legacy names (`soul.md`, `tools.md`, `identity.md`, `user.md`, `heartbeat.md`, `bootstrap.md`)

heartbeat behavior:
- disabled by default.
- enable per agent in `agents/<agent_id>/config.json` via:
  - `"heartbeat": { "every": "15m" }`
- optional heartbeat fields:
  - `"prompt"` custom poll prompt
  - `"ack_max_chars"` max chars to suppress when reply includes `HEARTBEAT_OK` (default `300`)
- if `user_timezone` in `config.json` is a valid IANA timezone, heartbeat dispatch is skipped during local sleeping hours (`22:00`-`08:00`)
- runtime sends heartbeat polls with explicit `target_actor_id`, so multi-agent sessions do not require `@agent` mention text for routing.
- when room=session includes multiple agents, heartbeat skips dispatch if the target agent's managed matrix user is not currently joined in that room.

web search MCP behavior:
- `web_search` is enabled by default for all agents at runtime.
- backing MCP endpoint: `https://api.z.ai/api/mcp/web_search_prime/mcp`
- tool aliases supported in `enabled_tools`: `web_search`, `search_mcp`, `search`, `group:web`
- per-agent opt-out in `agents/<agent_id>/config.json`:
  - `"disable_default_search_mcp": true`

example model policy:

```json
{
  "model_policy": "zai:glm-5"
}
```

## 2) gateway matrix config

in `/etc/gopher/gopher.toml`:

```toml
[gateway.matrix]
enabled = true
homeserver_url = "http://127.0.0.1:6167"
appservice_id = "gopher"
as_token = "<as_token>"
hs_token = "<hs_token>"
listen_addr = "127.0.0.1:29328"
bot_user_id = "@gopher:gophers.bostonc.dev"
presence_enabled = true
presence_interval = "60s"
presence_status_msg = ""
rich_text_enabled = true
```

runtime identity mapping:
- each agent workspace `agent_id` maps to `@<agent_id>:<domain from bot_user_id>`.
- with the config above, `agent_id = "gateway-agent"` maps to `@gateway-agent:gophers.bostonc.dev`.

rich text defaults to enabled and sends both plain `body` and formatted `formatted_body`.
disable it for compatibility checks with any of:
- toml: `rich_text_enabled = false`
- env: `GOPHER_GATEWAY_MATRIX_RICH_TEXT_ENABLED=false`
- cli: `--matrix-rich-text-enabled=false`

in `/etc/gopher/gopher.env`:

```bash
ZAI_API_KEY=<secret>
```

`ZAI_API_KEY` is required for the default `web_search` MCP tool.

restart service:

```bash
sudo systemctl restart gopher-gateway.service
gopher status
gopher logs --lines 200
```

## 3) conduit appservice registration

registration yaml path:
- `/etc/gopher/gopher-appservice-registration.yaml`

in matrix admin room:

1. send `@conduit:<server_name>: register-appservice`
2. paste yaml content
3. verify with `@conduit:<server_name>: list-appservices`

## 4) smoke test

run:

```bash
python3 scripts/matrix_dm_smoke.py \
  --homeserver http://127.0.0.1:6167 \
  --registration-token <registration_token> \
  --bot-user-id @gopher:<server_name>
```

success criteria:
- `bot_membership=join`
- `bot_reply_count>=1`

failure triage:
- `bot_membership=invite`: appservice registration mismatch or invite not reaching transport.
- `bot_reply_count=0` with `bot_membership=join`: provider auth missing/invalid or runtime execution failure.
