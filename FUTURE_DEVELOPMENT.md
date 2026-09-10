# Future Development

Planned work for Overworld Ops, roughly in the order it should be tackled.
Each item says *why* it matters, not just what to build — several of these are
gaps found while building the first pass, and the reasoning is the part that
would otherwise be lost.

Priorities:

- **P0** — correctness or safety gaps in code that already ships
- **P1** — the next real features
- **P2** — quality, ops and polish

---

## P0 — Fix before more people use it

### 1. Memory and CPU limits are neither validated nor bounded

Two related holes in [backend/internal/store/models.go](backend/internal/store/models.go)
and [backend/internal/docker/minecraft.go](backend/internal/docker/minecraft.go).

**`CreateServerInput.Validate()` never looks at `Memory`.** It is free text
from the client, passed straight into the container's `MEMORY` env var. It also
places no upper bound on `Memory` or `CPULimit` — `CPULimit` is only checked for
being negative. A friend can ask for `"64G"` and 32 cores, and the platform will
happily try to give it to them.

**Worse: a malformed memory string silently removes the memory cap.**

```go
func deriveMemoryLimit(heap string) int64 {
	bytes, err := parseSize(heap)
	if err != nil || bytes == 0 {
		return 0 // uncapped rather than wrongly capped
	}
	return bytes + (1 << 30)
}
```

`buildResources` assigns that to `container.Resources.Memory`, and **0 means
unlimited to Docker**. So `memory: "8 GB"` (a plausible typo — the parser wants
`8G`) produces a container with *no* memory limit at all. The failure mode is
the opposite of what a resource-governance feature should do when it is unsure.

Fix:

1. Validate `Memory` in `Validate()` by calling the same size parser, and
   reject anything that does not parse.
2. Add `OWO_MAX_MEMORY` and `OWO_MAX_CPU_LIMIT` config, enforced per server.
3. Make `deriveMemoryLimit` return an error rather than 0, and have
   `CreateServer` refuse to build a container it cannot cap.
4. Export the parser (it is currently unexported in `internal/docker`) or move
   it to a shared package so validation and container-building agree by
   construction.

This is the item that keeps one friend from taking down the box for everyone.

### 2. Login has no rate limiting

`POST /api/auth/login` will accept unlimited attempts. bcrypt makes each guess
expensive, which slows an attacker down — but it also turns the endpoint into a
cheap CPU-exhaustion vector: a few hundred concurrent login attempts will eat
every core the host has, and the Minecraft servers are on those same cores.

Add a small in-memory limiter keyed on IP *and* username (both, so one attacker
cannot lock out a real user by hammering their account). A `golang.org/x/time/rate`
limiter per key in a `sync.Map` with periodic eviction is enough; there is no
need for Redis here.

### 3. Stored status drifts from container reality

`serverView` decorates responses with live Docker state, so the *UI* is
accurate. But the `status` column itself is only written on explicit lifecycle
calls. A container that crashes on its own leaves the database saying
`running` forever.

Nothing depends on that today, which is exactly why it should be fixed before
something does — the idle reaper (P1) will read `status` and act on it.

Add a reconciler goroutine that periodically lists containers by the
`overworld-ops.managed=true` label, compares against the registry, and writes
back the truth. This is also where a container that vanished should flip its
server to `error` with a useful message.

### 4. Nothing reattaches after a restore

`docker.FindByServerID` was written for this and is currently dead code — the
compiler does not complain because it is exported, which is its own small
warning sign.

If the SQLite file is restored from a backup, or `container_id` is otherwise
lost, every server becomes unmanageable even though its container is running
fine. On startup: for each server with an empty or stale `container_id`, look
the container up by label and heal the record.

### 5. Port pool and published range can silently disagree

`OWO_PORT_RANGE_END` defaults to 25665, but [docker-compose.yml](docker-compose.yml)
publishes only `25565-25575`. Raise the pool without touching the compose file
and servers 11+ are created successfully, report a nice `host:port`, and are
simply unreachable — with nothing anywhere saying why.

Have the API log a loud warning at startup when it is running containerised and
the pool is wider than what looks published, or add an explicit
`OWO_PUBLISHED_PORT_RANGE` that must contain the pool.

### 6. Renaming a server does not update its MOTD

`specFor` bakes `MOTD: srv.Name` into the container environment at create time.
`PATCH /api/servers/{id}` updates the database only, so the server list in the
Minecraft client keeps showing the old name until the container is recreated.

Either recreate the container on rename (heavy-handed, and it disconnects
players), stop advertising the name as the MOTD, or make MOTD its own editable
field and apply it via RCON once that exists (see P1).

---

## P1 — SvelteKit conventions and the next features

### 7. Move the frontend to server loads and form actions

This is the largest planned change and it should happen before the UI grows any
further, because every route added in the current style is another one to
migrate.

**What is wrong with the current approach.** Everything is client-side `fetch`
from [frontend/src/lib/api.ts](frontend/src/lib/api.ts), with a rune class
holding the session. That means:

- **No SSR.** Every page renders empty, then fills in after hydration. The
  dashboard flashes "Loading servers…" on every visit.
- **The auth redirect flashes too.** `+layout.svelte` waits for `/auth/me` to
  resolve client-side before it can bounce anyone to `/login`.
- **Nothing works without JavaScript**, and more importantly, nothing works
  *while* JavaScript is loading.
- **CSRF protection is only `SameSite=Lax`.** SvelteKit form actions add an
  origin check on top of that for free.
- **Error handling is copy-pasted** into every component as
  `err instanceof Error ? err.message : '...'`.

**Target layout:**

```
src/hooks.server.ts                      resolve session -> event.locals.user
src/lib/server/api.ts                    server-side API client (forwards cookie)
src/app.d.ts                             declare App.Locals.user
src/routes/+layout.server.ts             load: { user }
src/routes/+page.server.ts               load: servers; actions: start/stop/delete
src/routes/new/+page.server.ts           actions: default (create)
src/routes/login/+page.server.ts         actions: login/register
src/routes/servers/[id]/+page.server.ts  load: one server; actions: start/stop/rename
src/routes/api/servers/[id]/logs/+server.ts   SSE passthrough (see below)
```

`src/lib/api.ts` shrinks to shared *types* only. The rune-based
`session.svelte.ts` goes away entirely — `locals` plus a layout load does the
same job with SSR.

**Step 1 — server-side API client.** The Go API is a different origin, so
SvelteKit's `event.fetch` will not forward the session cookie automatically.
Be explicit rather than relying on the dev proxy, so dev and production behave
identically and the Go API can sit on a private network in production:

```ts
// src/lib/server/api.ts
import { env } from '$env/dynamic/private';

const BASE = env.API_INTERNAL_URL ?? 'http://localhost:8080';

export async function apiFetch(
	event: { request: Request },
	path: string,
	init: RequestInit = {}
): Promise<Response> {
	return fetch(`${BASE}/api${path}`, {
		...init,
		headers: {
			...(init.body ? { 'content-type': 'application/json' } : {}),
			// The Go API authenticates on this cookie; nothing else identifies
			// the caller, so it has to be relayed on every hop.
			cookie: event.request.headers.get('cookie') ?? '',
			...init.headers
		}
	});
}
```

**Step 2 — resolve the session once, in hooks.** This removes the redirect
flash, because it runs before any load function:

```ts
// src/hooks.server.ts
import { redirect, type Handle } from '@sveltejs/kit';
import { apiFetch } from '$lib/server/api';

const PUBLIC_ROUTES = ['/login'];

export const handle: Handle = async ({ event, resolve }) => {
	const res = await apiFetch(event, '/auth/me');
	event.locals.user = res.ok ? await res.json() : null;

	if (!event.locals.user && !PUBLIC_ROUTES.includes(event.url.pathname)) {
		redirect(303, '/login');
	}
	return resolve(event);
};
```

Declare the type so `locals.user` is not `any`:

```ts
// src/app.d.ts
import type { User } from '$lib/api';

declare global {
	namespace App {
		interface Locals {
			user: User | null;
		}
	}
}
export {};
```

**Step 3 — loads replace `onMount` fetching.** Name the dependency so polling
can invalidate just this data instead of everything:

```ts
// src/routes/+page.server.ts
import { fail, type Actions, type PageServerLoad } from '@sveltejs/kit';
import { apiFetch } from '$lib/server/api';

export const load: PageServerLoad = async (event) => {
	event.depends('app:servers');
	const res = await apiFetch(event, '/servers');
	if (!res.ok) return { servers: [], error: 'could not load servers' };
	return { servers: await res.json() };
};

export const actions: Actions = {
	start: async (event) => {
		const id = String((await event.request.formData()).get('id'));
		const res = await apiFetch(event, `/servers/${id}/start`, { method: 'POST' });
		if (!res.ok) return fail(res.status, { error: (await res.json()).error });
		return { success: true };
	}
	// stop and delete follow the same shape
};
```

The page then polls with `invalidate`, which re-runs only that load:

```svelte
<script lang="ts">
	import { invalidate } from '$app/navigation';
	import { enhance } from '$app/forms';

	let { data, form } = $props();

	$effect(() => {
		const timer = setInterval(() => invalidate('app:servers'), 5000);
		return () => clearInterval(timer);
	});
</script>

{#each data.servers as server (server.id)}
	<form method="POST" action="?/start" use:enhance>
		<input type="hidden" name="id" value={server.id} />
		<button>Start</button>
	</form>
{/each}
```

**Gotchas that will bite during this migration:**

- **`redirect()` throws.** In the create action, `redirect(303, ...)` inside a
  `try { } catch { }` will be swallowed by your own error handling and turn
  into a spurious failure. Put the redirect after the try block, or rethrow
  anything matching `isRedirect`.
- **Login has to relay `Set-Cookie`.** The Go API issues the session cookie,
  but the browser is talking to SvelteKit. The login action must read
  `res.headers.getSetCookie()` and re-issue via `cookies.set(...)` with an
  explicit `path: '/'`, or the user will appear to log in and then immediately
  be bounced back to `/login`. This is the single most likely thing to go
  wrong.
- **`use:enhance` is not optional in practice.** Without it every button is a
  full page navigation, and the dashboard's polling makes that feel broken.
- **The SSE console cannot become a form action.** It stays an `EventSource`.
  To keep everything same-origin, add a passthrough route that streams the Go
  response body through unchanged:

  ```ts
  // src/routes/api/servers/[id]/logs/+server.ts
  export const GET: RequestHandler = async (event) => {
  	const upstream = await apiFetch(event, `/servers/${event.params.id}/logs`);
  	return new Response(upstream.body, {
  		headers: {
  			'content-type': 'text/event-stream',
  			'cache-control': 'no-cache',
  			connection: 'keep-alive'
  		}
  	});
  };
  ```

  Do not buffer it — returning `upstream.body` directly is what keeps it a
  stream.
- **Keep `OWO_CORS_ORIGIN` empty afterwards.** Once every call is server-side,
  the browser never talks to the Go API cross-origin, and leaving CORS enabled
  is just a wider surface than needed.

Once this lands, `vite.config.ts`'s `/api` proxy is only needed for the SSE
route in dev, and the deployment story gets simpler: SvelteKit is the only
thing the browser talks to.

### 8. Idle auto-stop

`OWO_IDLE_TIMEOUT` is read from config and `last_active_at` is tracked, but
nothing reaps. This is the feature that keeps a shared box from filling up with
servers nobody is playing on.

The blocker is knowing the player count. Options, in order of preference:

1. **Server-list ping** — the same protocol the Minecraft client uses to show
   player counts. No server-side configuration, works for every loader, and
   needs only a TCP connection to the port already allocated.
2. **RCON** — more capable (also gives console commands, see below), but has to
   be enabled and a password managed per server. `ENABLE_RCON` is currently
   hardcoded to `false` in `buildEnv`.

Do not infer activity from log output: a server with no players still logs
autosaves and keepalives, so the reaper would never fire.

Then a goroutine on a ticker: for each running server whose player count has
been zero for longer than the timeout, call the same stop path the API uses.
The UI should show *why* a server stopped, or people will think it crashed.

### 9. Console input

Containers are already created with `OpenStdin: true`, so most of the work is
done. Add `POST /api/servers/{id}/console` that attaches to the container and
writes a line to stdin.

Two things to get right: only the owner may send commands (the existing
ownership check covers it), and commands should be written to an audit log —
`/op` is a real privilege escalation inside the game.

RCON (see above) is the alternative and gives structured responses rather than
having to scrape stdout.

### 10. World backups

Nothing currently protects against `?deleteData=true` clicked by mistake, a
corrupted chunk, or a modpack update that eats a world. For a platform whose
whole value is "your friends' base is safe here", that is the biggest missing
feature.

Sketch: a scheduled job that, per server, runs `save-off` + `save-all` (via
console/RCON), snapshots the volume to a tarball, then `save-on`. Keep the last
N. Restoring is a new container against a restored volume, which the existing
`recreateContainer` path already almost supports.

### 11. Velocity proxy

Feature 8 from the original plan. One port for everything, routing by
hostname, so friends type `smp.example.com` instead of `example.com:25572`.

This changes the port allocator's role substantially — ports become internal
and no longer need to be host-published at all, which also removes the
compose port-range problem in P0 item 5.

---

## P2 — Quality, testing and ops

### 12. There are no tests for `internal/api`

`internal/docker`, `internal/ports` and `internal/store` are covered.
`internal/api` — the layer holding the ownership checks, the session
signing and the resource caps — has none.

That is the wrong place to have zero coverage. The ownership check in
particular (`lookup` returning 404 rather than 403 for another user's server)
is exactly the kind of logic that gets accidentally inverted in a refactor.

`httptest.NewServer` plus a fake Docker client behind an interface would cover
the handlers without needing a daemon. The `docker.Client` concrete type would
need to become an interface at the API boundary first.

### 13. No frontend tests, no CI

Neither exists. Once the form-actions migration lands, `@testing-library/svelte`
for components and Playwright for a create-start-stop-delete happy path would
be worth it.

A GitHub Actions workflow should run what `scripts/run.sh --check` already runs
locally: `gofmt -l`, `go vet`, `go test`, `svelte-check`. That script is
deliberately CI-shaped so the two cannot drift.

### 14. Modpack versions are not pinned

`MODRINTH_MODPACK` and `CF_SLUG` are set from a bare slug, so itzg resolves
"latest" at boot. A server that restarts months later can silently come back on
a newer pack version — which, for a modded world, can mean broken saves.

Store an optional version alongside the slug and pass it through.

### 15. Session revocation

Sessions are stateless signed cookies, so an individual one cannot be revoked
before it expires; rotating `OWO_SESSION_SECRET` logs everybody out at once.
Fine at current scale, and worth revisiting if this ever leaves the homelab.

### 16. Smaller cleanups

- `decodeJSON` passes `nil` as the `ResponseWriter` to `http.MaxBytesReader`.
  It is safe today (the type assertion inside simply fails), but it means an
  oversized body does not close the connection. Pass the real writer.
- `handleUpdateServer` does a read-modify-write of the whole row, so two
  concurrent updates can clobber each other. Narrow it to the fields that
  actually changed.
- Disk usage per world is unbounded. A quota, or at least a dashboard warning,
  would stop one server filling the host.
- Self-service password change. An admin can reset anybody's password from the
  admin panel, but a user cannot yet change their own.

---

## Development scripts

Two helpers live in [scripts/](scripts/):

| Command | What it does |
| --- | --- |
| `scripts/run.sh` | API + Vite dev server together, one Ctrl-C stops both |
| `scripts/run.sh --api` / `--web` | One side only |
| `scripts/run.sh --prod` | `docker compose up --build` |
| `scripts/run.sh --down` | `docker compose down` |
| `scripts/run.sh --check` | gofmt, vet, test, svelte-check — what CI should run |
| `scripts/kill-stray.sh` | Kill leftover dev processes from an interrupted session |
| `scripts/kill-stray.sh --dry-run` | Show what would be killed |
| `scripts/kill-stray.sh --containers` | Also remove platform-managed containers |

`kill-stray.sh` exists because an interrupted session — a closed terminal, an
agent that started the API in the background and never stopped it — leaves
`overworld-ops` holding `:8080` and `vite` holding `:5173`. The next `run.sh`
then fails with "address already in use", or worse, silently serves stale code
from the old binary.

It is deliberately conservative:

- It only kills processes whose executable is a project binary or whose command
  line points into this checkout. Port 5173 is the Vite default for *every*
  Vite project on the machine, so killing whatever holds it would eventually
  take out an unrelated dev server. Use `--force` to override.
- It never touches Docker containers unless asked with `--containers`, because
  stopping one disconnects whoever is playing.
- It never removes world volumes. Even `--containers` leaves the data behind;
  removing a world stays a manual, deliberate act.
