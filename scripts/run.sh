#!/usr/bin/env bash
#
# Run the Overworld Ops stack.
#
# Default mode runs the Go API on the host and the Vite dev server beside it,
# with both logs interleaved in this terminal and a single Ctrl-C stopping
# both. The API runs on the host on purpose: the port allocator's bind test
# only means something when it runs in the Docker host's network namespace.
#
# Usage:
#   scripts/run.sh                # dev: API + Vite, hot reload
#   scripts/run.sh --api          # API only
#   scripts/run.sh --web          # frontend only
#   scripts/run.sh --prod         # docker compose up --build
#   scripts/run.sh --down         # docker compose down
#   scripts/run.sh --check        # build, vet, test, typecheck — no servers

set -uo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

MODE=dev

usage() {
	sed -n '2,/^set -uo/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//; $d'
	exit "${1:-0}"
}

while [ $# -gt 0 ]; do
	case "$1" in
		--api) MODE=api ;;
		--web) MODE=web ;;
		--prod) MODE=prod ;;
		--down) MODE=down ;;
		--check) MODE=check ;;
		--help | -h) usage 0 ;;
		*) err "unknown option: $1"; usage 1 ;;
	esac
	shift
done

# --- environment -------------------------------------------------------------

# .env is optional; without it the config defaults apply and the API warns
# about the well-known session secret.
load_env() {
	[ -f "$REPO_ROOT/.env" ] || return 0

	info "loading .env"
	# Export every KEY=VALUE line, ignoring comments and blanks. `set -a` marks
	# assignments for export so the children inherit them.
	set -a
	# shellcheck disable=SC1091
	. "$REPO_ROOT/.env"
	set +a
}

# preflight checks the things whose absence produces a confusing failure ten
# seconds later rather than an obvious one now.
preflight() {
	require go "install from https://go.dev/dl/"

	if ! docker info >/dev/null 2>&1; then
		die "Docker daemon is not reachable — start Docker Desktop and retry"
	fi

	local busy
	busy=$(pids_on_port "$API_PORT")
	if [ -n "$busy" ]; then
		err "port $API_PORT is already in use by $(describe_pid "$(echo "$busy" | head -1)")"
		die "run scripts/kill-stray.sh first"
	fi
}

# --- modes -------------------------------------------------------------------

run_api() {
	info "starting Go API on :$API_PORT"
	cd "$REPO_ROOT/backend" || die "backend directory missing"
	exec go run ./cmd/overworld-ops -debug
}

run_web() {
	require node "install Node 20 or newer"

	cd "$REPO_ROOT/frontend" || die "frontend directory missing"
	if [ ! -d node_modules ]; then
		info "installing frontend dependencies"
		npm install || die "npm install failed"
	fi

	info "starting Vite dev server on :$WEB_DEV_PORT"
	exec npm run dev
}

run_dev() {
	load_env
	preflight
	require node "install Node 20 or newer"

	if [ ! -d "$REPO_ROOT/frontend/node_modules" ]; then
		info "installing frontend dependencies"
		(cd "$REPO_ROOT/frontend" && npm install) || die "npm install failed"
	fi

	# Track both children so a single Ctrl-C takes the whole stack down. Without
	# this the Go process survives and holds :8080 for the next run — the exact
	# situation kill-stray.sh exists to clean up.
	local api_pid="" web_pid=""

	shutdown() {
		echo
		info "shutting down"
		[ -n "$web_pid" ] && kill_pid "$web_pid" >/dev/null 2>&1
		[ -n "$api_pid" ] && kill_pid "$api_pid" >/dev/null 2>&1
		wait 2>/dev/null
		ok "stopped"
	}
	trap shutdown INT TERM EXIT

	info "starting Go API on :$API_PORT"
	(cd "$REPO_ROOT/backend" && go run ./cmd/overworld-ops -debug 2>&1 | sed "s/^/${C_BLUE}[api]${C_RESET} /") &
	api_pid=$!

	info "starting Vite dev server on :$WEB_DEV_PORT"
	(cd "$REPO_ROOT/frontend" && npm run dev 2>&1 | sed "s/^/${C_GREEN}[web]${C_RESET} /") &
	web_pid=$!

	echo
	ok "dashboard   http://localhost:$WEB_DEV_PORT"
	ok "API health  http://localhost:$API_PORT/api/health"
	dim "the first account you register becomes the admin"
	dim "Ctrl-C stops both"
	echo

	# Exit as soon as either side dies, so a crashed API is not hidden behind a
	# still-running frontend.
	wait -n "$api_pid" "$web_pid" 2>/dev/null || wait "$api_pid" "$web_pid" 2>/dev/null
}

run_prod() {
	require docker
	load_env

	[ -f "$REPO_ROOT/.env" ] || warn "no .env — compose will fail without OWO_SESSION_SECRET"

	info "building and starting the compose stack"
	cd "$REPO_ROOT" || exit 1
	docker compose up --build
}

run_down() {
	require docker
	info "stopping the compose stack"
	cd "$REPO_ROOT" || exit 1
	docker compose down
	dim "game containers are siblings, not compose services — they keep running"
	dim "use scripts/kill-stray.sh --containers to remove those too"
}

run_check() {
	local failed=0

	info "backend: gofmt"
	local unformatted
	unformatted=$(cd "$REPO_ROOT/backend" && gofmt -l .)
	if [ -n "$unformatted" ]; then
		err "these files need gofmt:"
		echo "$unformatted"
		failed=1
	else
		ok "formatted"
	fi

	info "backend: go vet"
	(cd "$REPO_ROOT/backend" && go vet ./...) && ok "vet clean" || failed=1

	info "backend: go test"
	(cd "$REPO_ROOT/backend" && go test ./...) && ok "tests pass" || failed=1

	if [ -d "$REPO_ROOT/frontend/node_modules" ]; then
		info "frontend: svelte-check"
		(cd "$REPO_ROOT/frontend" && npm run check) && ok "typecheck clean" || failed=1
	else
		warn "frontend dependencies not installed — skipping svelte-check"
	fi

	echo
	if [ "$failed" = "0" ]; then
		ok "all checks passed"
	else
		die "some checks failed"
	fi
}

case "$MODE" in
	dev) run_dev ;;
	api) load_env; preflight; run_api ;;
	web) run_web ;;
	prod) run_prod ;;
	down) run_down ;;
	check) run_check ;;
esac
