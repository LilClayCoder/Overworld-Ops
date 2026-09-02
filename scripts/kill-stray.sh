#!/usr/bin/env bash
#
# Kill development processes left running by an interrupted session — an agent
# that started the API in the background and never stopped it, a `npm run dev`
# whose terminal was closed, a Go build still holding the database file.
#
# The symptom is almost always the same: the next `run.sh` fails with "address
# already in use", or the API serves stale code because an old binary still
# owns port 8080.
#
# By default this only touches processes holding the project's dev ports and
# processes whose command line points into this repository. Docker containers
# are left alone unless you ask for them, because stopping one disconnects
# whoever is playing on it.
#
# Usage:
#   scripts/kill-stray.sh                 # kill stray dev processes
#   scripts/kill-stray.sh --dry-run       # show what would be killed
#   scripts/kill-stray.sh --containers    # also remove test Minecraft containers
#   scripts/kill-stray.sh --all           # processes + containers, no prompts
#   scripts/kill-stray.sh --yes           # skip confirmation prompts
#   scripts/kill-stray.sh --force         # kill port holders from other projects too

set -uo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

DRY_RUN=0
DO_CONTAINERS=0
ASSUME_YES=0
FORCE=0

usage() {
	sed -n '2,/^set -uo/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//; $d'
	exit "${1:-0}"
}

while [ $# -gt 0 ]; do
	case "$1" in
		--dry-run | -n) DRY_RUN=1 ;;
		--containers) DO_CONTAINERS=1 ;;
		--all) DO_CONTAINERS=1; ASSUME_YES=1 ;;
		--yes | -y) ASSUME_YES=1 ;;
		--force | -f) FORCE=1 ;;
		--help | -h) usage 0 ;;
		*) err "unknown option: $1"; usage 1 ;;
	esac
	shift
done

[ "$DRY_RUN" = "1" ] && warn "dry run — nothing will actually be killed"

KILLED=0
FOUND=0

# PIDs already handled. The port scan and the stray-binary scan overlap — the
# API holds :8080 *and* is named overworld-ops — so without this the summary
# double-counts one process.
SEEN_PIDS=" "

seen() {
	case "$SEEN_PIDS" in
		*" $1 "*) return 0 ;;
	esac
	SEEN_PIDS="$SEEN_PIDS$1 "
	return 1
}

# --- processes holding the dev ports -----------------------------------------

info "checking dev ports"

for entry in \
	"$API_PORT:Go API" \
	"$WEB_DEV_PORT:Vite dev server" \
	"$WEB_PREVIEW_PORT:Vite preview" \
	"$WEB_PROD_PORT:SvelteKit node server"; do

	port="${entry%%:*}"
	label="${entry#*:}"

	pids=$(pids_on_port "$port")
	if [ -z "$pids" ]; then
		dim "  :$port free ($label)"
		continue
	fi

	for pid in $pids; do
		# PID 0 and 4 are kernel-owned on Windows; never touch them.
		if [ "$pid" = "0" ] || [ "$pid" = "4" ]; then
			warn "  :$port held by a system process (pid $pid) — skipping"
			continue
		fi

		desc=$(describe_pid "$pid")

		# Only kill processes that belong to this checkout, unless --force.
		belongs_to_repo "$pid"
		case "$?" in
			1)
				if [ "$FORCE" = "1" ]; then
					warn "  :$port held by $desc from another project — killing anyway (--force)"
				else
					warn "  :$port held by $desc, which is NOT from this repo — skipping"
					dim "        pass --force to kill it regardless"
					continue
				fi
				;;
			2)
				# Common for processes owned by another user, or when the CIM
				# query is unavailable. Say so rather than silently guessing.
				warn "  :$port held by $desc — could not read its command line"
				if [ "$FORCE" != "1" ]; then
					dim "        skipping; pass --force to kill it anyway"
					continue
				fi
				;;
		esac

		seen "$pid" && continue
		FOUND=$((FOUND + 1))

		if [ "$DRY_RUN" = "1" ]; then
			printf '  would kill %s on :%s (%s)\n' "$desc" "$port" "$label"
			continue
		fi

		if kill_pid "$pid"; then
			ok "killed $desc on :$port ($label)"
			KILLED=$((KILLED + 1))
		else
			err "could not kill $desc on :$port — try an elevated shell"
		fi
	done
done

# --- stray binaries and toolchain processes ----------------------------------
#
# A build can leave an overworld-ops binary running without holding a port —
# for example when it failed at startup after the Docker ping but before
# ListenAndServe, or when it was started with a different OWO_HTTP_ADDR.

info "checking for stray overworld-ops processes"

stray_pids() {
	if [ "$IS_WINDOWS" = "1" ]; then
		tasklist //FI "IMAGENAME eq overworld-ops.exe" //FO CSV //NH 2>/dev/null |
			grep -v '^INFO:' | cut -d, -f2 | tr -d '"'
	else
		pgrep -f '(^|/)overworld-ops($| )' 2>/dev/null
	fi
}

found_stray=0
for pid in $(stray_pids); do
	[ -n "$pid" ] || continue
	seen "$pid" && continue
	found_stray=1
	FOUND=$((FOUND + 1))
	desc=$(describe_pid "$pid")

	if [ "$DRY_RUN" = "1" ]; then
		printf '  would kill %s\n' "$desc"
		continue
	fi

	if kill_pid "$pid"; then
		ok "killed $desc"
		KILLED=$((KILLED + 1))
	else
		err "could not kill $desc"
	fi
done
[ "$found_stray" = "0" ] && dim "  none found"

# --- test containers ---------------------------------------------------------

if [ "$DO_CONTAINERS" = "1" ]; then
	info "checking for platform-managed Minecraft containers"

	if ! command -v docker >/dev/null 2>&1; then
		warn "docker not on PATH — skipping container cleanup"
	elif ! docker info >/dev/null 2>&1; then
		warn "docker daemon unreachable — skipping container cleanup"
	else
		# Only ever consider containers this platform created. The label is
		# stamped by internal/docker at create time.
		mapfile -t containers < <(
			docker ps -aq --filter "label=overworld-ops.managed=true" 2>/dev/null
		)

		if [ "${#containers[@]}" -eq 0 ]; then
			dim "  none found"
		else
			printf '  %d container(s):\n' "${#containers[@]}"
			docker ps -a --filter "label=overworld-ops.managed=true" \
				--format '    {{.Names}}  {{.Status}}' 2>/dev/null

			# Removing a container is not the same as losing a world — the data
			# lives in a separate named volume that this script never touches —
			# but it does disconnect anyone currently playing.
			if [ "$DRY_RUN" = "1" ]; then
				printf '  would remove the containers above (world volumes kept)\n'
			elif [ "$ASSUME_YES" = "1" ] || confirm "  remove these containers? (world volumes are kept)"; then
				for cid in "${containers[@]}"; do
					name=$(docker inspect "$cid" --format '{{.Name}}' 2>/dev/null | sed 's|^/||')
					if docker rm -f "$cid" >/dev/null 2>&1; then
						ok "removed container ${name:-$cid}"
						KILLED=$((KILLED + 1))
					else
						err "could not remove container ${name:-$cid}"
					fi
				done
				dim "  world volumes kept — list them with:"
				dim "    docker volume ls --filter label=overworld-ops.managed=true"
			else
				dim "  skipped"
			fi
		fi
	fi
else
	dim "(containers left alone; pass --containers to clean those up too)"
fi

# --- summary -----------------------------------------------------------------

echo
if [ "$DRY_RUN" = "1" ]; then
	info "dry run complete — $FOUND process(es) would be affected"
elif [ "$FOUND" -eq 0 ]; then
	ok "nothing stray found"
else
	info "done — killed $KILLED of $FOUND"
fi
