# Gopher Admin Console

React + TypeScript + Vite frontend for the current Gopher admin and local chat surfaces, using the `shadcn` preset `aw2hLHE` as the design baseline.

## Run it

Start the Go API server (mock panel) first from the repo root:

```bash
go run ./cmd/gopher panel mock --dev-spa-url=http://127.0.0.1:4010
```

Then run the frontend dev server:

```bash
cd frontend/admin
bun run dev
```

The React SPA runs at [http://127.0.0.1:4010](http://127.0.0.1:4010). The `--dev-spa-url` flag tells the Go server to redirect `/sessions`, `/automations`, and `/chat` routes to the React SPA instead of serving Go's embedded templates.

When you visit:
- [http://127.0.0.1:4010/](http://127.0.0.1:4010/) → redirects to `/chat`
- [http://127.0.0.1:4010/chat](http://127.0.0.1:4010/chat) - Local chat surface
- [http://127.0.0.1:4010/sessions](http://127.0.0.1:4010/sessions) - Work sessions view
- [http://127.0.0.1:4010/automations](http://127.0.0.1:4010/automations) - Cron jobs view

API calls proxy to the Go panel at `http://127.0.0.1:39400`.

## Useful commands

```bash
bun run dev
bun run typecheck
bun run build
bun run lint
```

## Proxy target

To point the frontend at a different panel instance:

```bash
GOPHER_PANEL_PROXY_TARGET=http://127.0.0.1:39558 bun run dev
```

The frontend currently fetches live data from:

- `/sessions/api/work/sessions`
- `/sessions/api/work/session/:sessionId`
- `/automations/api/automations`
- `/chat/api/sessions`
- `/chat/api/session/:sessionId`
