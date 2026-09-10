8. Admin panel — accounts (create, promote, demote, reset password, delete
   with server reassignment) and every server on the box, at `/admin`
| `GET` | `/api/admin/users` | List accounts with server counts (admin only) |
| `POST` | `/api/admin/users` | Create an account without logging yourself out (admin only) |
| `PATCH` | `/api/admin/users/{id}` | Promote, demote, or reset a password (admin only) |
| `DELETE` | `/api/admin/users/{id}` | Delete an account; `?reassign=true` moves their servers to you first (admin only) |
# Overworld Ops

Self-service Minecraft server deployment for a homelab. Friends log in, pick a
modloader or a modpack, and get a running server with a `host:port` to connect
to — no terminal, no SSH, no asking you to do it.

Vanilla, Forge, Fabric and NeoForge are supported, plus CurseForge and Modrinth
modpacks, because each server is an [`itzg/minecraft-server`][itzg] container
and that image handles every loader and pack download from environment
variables alone.

[itzg]: https://github.com/itzg/docker-minecraft-server

## Architecture

```
Browser
   │
   ▼
SvelteKit frontend  ──────────►  Go REST API  ──────────►  Docker Engine API
 (dashboard, forms,               (auth, registry,          (create / start /
  SSE console)                     ports, lifecycle)         stop / rm, logs)
                                        │                          │
                                        ▼                          ▼
                                   SQLite registry      itzg/minecraft-server
                                                        containers, one named
                                                        volume each
```

The API creates game containers as **siblings** on the Docker host, not
children of itself. They survive an API restart, which matters: restarting the
control plane must never disconnect players.

Each server gets:

- its own container, named `owo-mc-<server-id>`
- its own named volume, `owo-data-<server-id>`, holding `/data` (the world)
- one host port from a configured pool, forwarded to the container's 25565
- a CPU and memory cap
- `restart: unless-stopped`, so servers come back after a host reboot but stay
  down when someone stopped them on purpose

Container and volume names derive from the server **ID**, never the display
name, so two servers called "SMP" cannot collide and a rename is free.

### Repository layout

```
backend/
  cmd/overworld-ops/      entrypoint: config, wiring, graceful shutdown
  internal/config/        environment-variable configuration
  internal/docker/        Docker Engine wrapper (the core loop) + log demuxing
  internal/api/           REST handlers, session auth, SSE console
  internal/store/         SQLite registry: users and servers
  internal/ports/         host port pool allocator
frontend/
  src/lib/                typed API client, session state
  src/routes/             dashboard, create form, login, per-server console
scripts/
  run.sh                  start the stack (dev, prod, or checks)
  kill-stray.sh           clean up processes left by an interrupted session
docker-compose.yml        local dev stack
```

## Running it

### Prerequisites

Docker, Go 1.24+, Node 20+.

### Development

Copy the environment template and set a session secret:

```sh
cp .env.example .env
# then edit OWO_SESSION_SECRET — `openssl rand -hex 32` gives you one
```

Then start both halves of the stack:

```sh
scripts/run.sh
```

That runs the Go API on `:8080` and the Vite dev server on `:5173`, interleaves
their logs, and stops both on Ctrl-C. Open http://localhost:5173 — the first
account to register becomes the admin. Admins get an **Admin** tab for managing
accounts and every server on the box.

The API runs on the host rather than in a container on purpose: the port
allocator's bind test only means anything in the Docker host's own network
namespace.

The Vite dev server proxies `/api` to `http://localhost:8080`, so the browser
sees a single origin and the session cookie works without any CORS setup.

Other modes:

```sh
scripts/run.sh --api      # API only
scripts/run.sh --web      # frontend only
scripts/run.sh --check    # gofmt, vet, go test, svelte-check
scripts/run.sh --prod     # docker compose up --build
scripts/run.sh --down     # docker compose down
```

If a previous session was interrupted and left the ports held:

```sh
scripts/kill-stray.sh --dry-run   # see what is stray
scripts/kill-stray.sh             # kill it
```

It only touches processes belonging to this checkout, and never removes world
volumes. See [FUTURE_DEVELOPMENT.md](FUTURE_DEVELOPMENT.md#development-scripts)
for the full flag list.

### Docker Compose

```sh
docker compose up --build
```

The frontend lands on http://localhost:3000 and the API on
http://localhost:8080.

Two things to know about running the API in a container:

- It mounts `/var/run/docker.sock`. That is **effectively root on the host** —
  fine for a box you own, not something to expose to the internet.
- Game containers publish their ports on the host, so the `api` service
  publishes the whole pool range (`25565-25575` by default). Widen that
  mapping if you raise `OWO_PORT_RANGE_END`, or the extra servers will be
  created but unreachable.

## Configuration

Everything is environment variables, with defaults sized for one homelab box.

| Variable | Default | What it does |
| --- | --- | --- |
| `OWO_HTTP_ADDR` | `:8080` | API listen address |
| `OWO_DATABASE_URL` | `./data/overworld.db` | SQLite file path |
| `OWO_DOCKER_HOST` | *(from environment)* | Override the Engine endpoint |
| `OWO_MINECRAFT_IMAGE` | `itzg/minecraft-server:latest` | Image every server runs |
| `OWO_PORT_RANGE_START` | `25565` | First host port in the pool |
| `OWO_PORT_RANGE_END` | `25665` | Last host port in the pool |
| `OWO_MAX_SERVERS` | `10` | Hard cap on server records |
| `OWO_DEFAULT_MEMORY` | `2G` | JVM heap when the form leaves it blank |
| `OWO_DEFAULT_CPU_LIMIT` | `2.0` | Fractional cores per container |
| `OWO_IDLE_TIMEOUT` | `30m` | Idle window before auto-stop *(not yet enforced)* |
| `OWO_STOP_TIMEOUT` | `60s` | Grace period to save the world on stop |
| `OWO_PROBE_HOST_PORTS` | `true` | Bind-test candidate ports; **set false in a container** |
| `OWO_PUBLIC_HOSTNAME` | `localhost` | Address shown to players |
| `OWO_SESSION_SECRET` | *(insecure dev value)* | HMAC key for session cookies |
| `OWO_SECURE_COOKIES` | `false` | Set true behind TLS |
| `OWO_ALLOW_REGISTRATION` | `true` | Open sign-up |
| `OWO_ADMIN_USERNAME` / `OWO_ADMIN_PASSWORD` | *(empty)* | Seed the first admin |
| `OWO_CURSEFORGE_API_KEY` | *(empty)* | Required for CurseForge packs only |
| `OWO_CORS_ORIGIN` | `http://localhost:5173` | Dev-server origin allowed to send cookies |

## API

All routes are under `/api`. Everything except `/api/health` and the auth
endpoints requires a session cookie.

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/health` | Liveness, including whether Docker is reachable |
| `POST` | `/api/auth/register` | Create an account (first one becomes admin) |
| `POST` | `/api/auth/login` | Exchange credentials for a session cookie |
| `POST` | `/api/auth/logout` | Clear the session |
| `GET` | `/api/auth/me` | Current user |
| `GET` | `/api/servers` | List your servers (admins see all) |
| `POST` | `/api/servers` | Create a server: allocate port, write record, create container |
| `GET` | `/api/servers/{id}` | One server, with live container state |
| `PATCH` | `/api/servers/{id}` | Rename |
| `DELETE` | `/api/servers/{id}` | Remove container; `?deleteData=true` also destroys the world |
| `POST` | `/api/servers/{id}/start` | Start the container |
| `POST` | `/api/servers/{id}/stop` | Stop it, with a grace period to save |
| `GET` | `/api/servers/{id}/logs` | SSE console stream (`?tail=200`) |
| `GET` | `/api/admin/users` | List accounts with server counts (admin only) |
| `POST` | `/api/admin/users` | Create an account without logging yourself out (admin only) |
| `PATCH` | `/api/admin/users/{id}` | Promote, demote, or reset a password (admin only) |
| `DELETE` | `/api/admin/users/{id}` | Delete an account; `?reassign=true` moves their servers to you first (admin only) |

### Auth model

A session is an HMAC-signed cookie carrying a user ID and an expiry — no
session table. For a handful of friends that is enough, and it keeps restarts
simple. The trade-offs, stated plainly:

- Sessions cannot be revoked one at a time. Rotating `OWO_SESSION_SECRET`
  invalidates all of them at once.
- Ownership is scoped per user: you only see and control servers you created.
  Admins see everything. Requests for someone else's server return 404, not
  403, so the API never confirms that an ID exists.

## Status

**Working end to end:**

1. Docker lifecycle — create, start, stop, delete a server container
2. Server registry and full REST CRUD
3. Port allocation from a bounded pool, with a `UNIQUE` constraint as the
   backstop against two servers claiming a port
4. SvelteKit UI — login, create form, dashboard, start/stop, connection info
5. Live console streaming over SSE
6. Modpack support — Modrinth and CurseForge IDs pass through to itzg
7. Resource governance — *partial*: per-container CPU and memory caps and a
   max-server cap are enforced
8. Admin panel at `/admin` — accounts (create, promote, demote, reset password,
   delete with server reassignment) and every server on the box

**Not built yet**, in priority order — see
[FUTURE_DEVELOPMENT.md](FUTURE_DEVELOPMENT.md) for the detail and the reasoning:

- **Known gaps in shipped code.** Memory and CPU requests are unvalidated and
  unbounded, and a malformed memory string silently produces an *uncapped*
  container. Login has no rate limiting. Stored status drifts from container
  reality.
- **Frontend on SvelteKit conventions.** Data loading and mutations are
  client-side `fetch` today; they should be `+page.server.ts` loads and form
  actions, for SSR, progressive enhancement and CSRF protection.
- **Idle auto-stop.** `OWO_IDLE_TIMEOUT` is read and `last_active_at` is
  tracked, but nothing reaps idle servers yet.
- **World backups**, **console input**, **startup reconciliation**, and the
  **Velocity proxy** (feature 8).

### Notes on the design

A few decisions worth knowing about before extending this:

- **Memory caps add headroom.** The container limit is the JVM heap plus 1 GiB.
  Capping the container at exactly the heap size gets the JVM OOM-killed
  mid-chunk-save, which corrupts worlds — metaspace, the JIT code cache and GC
  structures all live outside the heap.
- **Deleting a server keeps the world by default.** `?deleteData=true` is
  opt-in, and the UI asks about it in a separate confirmation, because losing a
  world to a misread dialog is not recoverable.
- **"Running" is not "joinable".** A container is up long before the server
  accepts connections; a large modpack can take several minutes. The UI trusts
  the image's healthcheck (`starting` → `healthy`) rather than container state.
- **A modpack overrides the chosen loader and version.** The pack manifest
  pins both, so the create form hides those inputs once a pack is selected.
