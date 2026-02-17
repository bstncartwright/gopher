# matrix single-agent ops runbook

this runbook is the repeatable setup path for one matrix-chatable agent on a gateway vm.

## prerequisites

- conduit homeserver reachable from gateway
- `gopher-gateway.service` installed
- provider key available (example: `ZAI_API_KEY`)

## 1) runtime workspace validation

required files under the service working directory (`/home/exedev/.gopher`):

- `AGENTS.md`
- `soul.md`
- `config.json`
- `policies.json`

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
```

in `/etc/gopher/gopher.env`:

```bash
ZAI_API_KEY=<secret>
```

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
