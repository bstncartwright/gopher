# agent lifecycle phase 2

this document defines the minimum surface for moving from single-agent dm to your target model: one matrix identity per agent, plus dedicated task rooms.

## goals

- keep phase-1 single-agent dm stable
- add explicit agent registry and lifecycle commands
- route dm chats and task rooms deterministically

## data model

registry path:
- `~/.gopher/agents/index.json`

entry shape:
- `agent_id` (stable id)
- `matrix_user_id` (e.g. `@planner:gophers.bostonc.dev`)
- `workspace_path` (e.g. `~/.gopher/agents/planner`)
- `status` (`active`, `disabled`, `deleted`)
- `created_at`, `updated_at`

workspace layout per agent:
- `~/.gopher/agents/USER.md` (shared user profile applied across agents)
- `~/.gopher/agents/<agent_id>/AGENTS.md`
- `~/.gopher/agents/<agent_id>/SOUL.md`
- `~/.gopher/agents/<agent_id>/TOOLS.md`
- `~/.gopher/agents/<agent_id>/IDENTITY.md`
- `~/.gopher/agents/<agent_id>/USER.md` (optional agent-local overrides)
- `~/.gopher/agents/<agent_id>/HEARTBEAT.md` (optional; used when heartbeat is configured)
- `~/.gopher/agents/<agent_id>/BOOTSTRAP.md` (brand-new workspaces)
- `~/.gopher/agents/<agent_id>/MEMORY.md` (optional)
- `~/.gopher/agents/<agent_id>/config.json`
- `~/.gopher/agents/<agent_id>/policies.json`

compatibility:
- runtime prefers uppercase canonical bootstrap files
- runtime falls back to lowercase legacy names when canonical files are missing

## cli surface

- `gopher agent create --id <agent_id> --matrix-user @<id>:<server>`
- `gopher agent list`
- `gopher agent delete --id <agent_id> [--hard=false]`

behavior:
- `create` allocates workspace + adds registry entry
- `delete` defaults to soft-delete (status only)
- `--hard` optional permanent file removal

## routing model

dm routing:
- key: `(human_user_id, agent_id)`
- map to one long-lived session id
- reused for standard dm chat continuity

task routing:
- task command in dm triggers task session creation
- gateway creates a dedicated matrix room for task execution
- room metadata stores parent dm and agent linkage

## migration strategy

1. keep existing single-agent path as fallback.
2. introduce registry reads behind feature flag.
3. route only when `agent_id` can be resolved unambiguously.
4. add metrics/logging for route decisions and session mapping.

## risks

- appservice namespace collisions across multiple agent matrix users
- accidental fanout when dm agent resolution fails
- operational confusion without clear `agent list` status output

## done criteria

- create/list/delete commands operate on registry + workspaces
- dm to each agent resolves to its own long-lived session
- task trigger creates separate room + session per task

## node onboarding checklist

1. install node runtime on a worker vm:
   - `GOPHER_GITHUB_TOKEN=... ./scripts/install.sh --role node`
2. verify node service state:
   - `gopher logs --unit gopher-node.service --lines 200`
3. validate nats connectivity:
   - confirm `node running` startup log with expected `nats_url`
4. verify capability registration:
   - on gateway logs, confirm capability/heartbeat updates for the node id
5. verify remote execution:
   - set an agent `config.json` capability requirement:
     - `"execution": { "required_capabilities": ["tool:gpu"] }`
   - send a message through that agent and confirm execution is routed to the worker node
