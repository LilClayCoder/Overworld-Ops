#!/usr/bin/env bash
# Shared helpers for the Overworld Ops dev scripts.
#
# These run under Git Bash on Windows as well as a normal POSIX shell, because
# the project is developed on Windows but deployed on Linux. Anything
# platform-specific is branched on $IS_WINDOWS rather than assumed.

# Resolve the repository root from this file's location, so the scripts work
# no matter which directory they are invoked from.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export REPO_ROOT

case "$(uname -s)" in
	MINGW* | MSYS* | CYGWIN*) IS_WINDOWS=1 ;;
	*) IS_WINDOWS=0 ;;
esac
export IS_WINDOWS

# Ports the dev stack uses. Keep in sync with vite.config.ts and the defaults
# in backend/internal/config.
API_PORT="${API_PORT:-8080}"
WEB_DEV_PORT="${WEB_DEV_PORT:-5173}"
WEB_PREVIEW_PORT="${WEB_PREVIEW_PORT:-4173}"
WEB_PROD_PORT="${WEB_PROD_PORT:-3000}"
export API_PORT WEB_DEV_PORT WEB_PREVIEW_PORT WEB_PROD_PORT

# Colours, disabled when stdout is not a terminal so logs stay readable.
if [ -t 1 ]; then
	C_RESET=$'\033[0m'
	C_DIM=$'\033[2m'
	C_RED=$'\033[31m'
	C_GREEN=$'\033[32m'
	C_YELLOW=$'\033[33m'
	C_BLUE=$'\033[34m'
else
	C_RESET='' C_DIM='' C_RED='' C_GREEN='' C_YELLOW='' C_BLUE=''
fi

info() { printf '%s==>%s %s\n' "$C_BLUE" "$C_RESET" "$*"; }
ok() { printf '%s  ok%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warn() { printf '%swarn%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
err() { printf '%s err%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; }
dim() { printf '%s%s%s\n' "$C_DIM" "$*" "$C_RESET"; }

die() {
	err "$*"
	exit 1
}

# require CMD [HINT] — exit with a useful message if a tool is missing.
require() {
	command -v "$1" >/dev/null 2>&1 || die "$1 not found on PATH${2:+ — $2}"
}

# pids_on_port PORT — print the PIDs listening on a TCP port, one per line.
#
# Windows and Linux need entirely different tooling here: netstat's output
# format differs, and Git Bash has no lsof or ss.
pids_on_port() {
	local port="$1"

	if [ "$IS_WINDOWS" = "1" ]; then
		# Match ":<port>" in the local-address column and LISTENING state, then
		# take the trailing PID column. The address column may be 0.0.0.0,
		# 127.0.0.1, [::] or similar, so anchor on the colon.
		netstat -ano 2>/dev/null |
			awk -v p=":$port\$" '$1 == "TCP" && $2 ~ p && $4 == "LISTENING" { print $5 }' |
			sort -u
		return
	fi

	if command -v lsof >/dev/null 2>&1; then
		lsof -ti "tcp:$port" -sTCP:LISTEN 2>/dev/null | sort -u
	elif command -v ss >/dev/null 2>&1; then
		ss -lptnH "sport = :$port" 2>/dev/null |
			grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u
	fi
}

# Executable names this project owns outright. A process with one of these
# names is ours wherever it lives on disk — which matters because `go run`
# compiles to a temp directory, so the binary's path says nothing about the
# repository it was built from.
PROJECT_BINARIES="overworld-ops overworld-ops.exe"

# process_name PID — print a process's executable name, or "unknown".
process_name() {
	local pid="$1" name=""

	if [ "$IS_WINDOWS" = "1" ]; then
		name=$(tasklist //FI "PID eq $pid" //FO CSV //NH 2>/dev/null |
			head -1 | cut -d, -f1 | tr -d '"')
	else
		name=$(ps -p "$pid" -o comm= 2>/dev/null)
	fi

	# tasklist prints "INFO: No tasks..." to stdout when nothing matches.
	[ -n "$name" ] && [ "$name" != "INFO:" ] || name="unknown"
	printf '%s' "$name"
}

# describe_pid PID — print a short "name (pid N)" label, best effort.
describe_pid() {
	printf '%s (pid %s)' "$(process_name "$1")" "$1"
}

# process_cmdline PID — print a process's full command line, or nothing if it
# cannot be determined.
process_cmdline() {
	local pid="$1"

	if [ "$IS_WINDOWS" = "1" ]; then
		# tasklist cannot show a command line; CIM can. Failures are silent
		# because this is only ever used to make a kill safer, never required.
		powershell -NoProfile -NonInteractive -Command \
			"(Get-CimInstance Win32_Process -Filter 'ProcessId=$pid' -ErrorAction SilentlyContinue).CommandLine" \
			2>/dev/null | tr -d '\r'
	elif [ -r "/proc/$pid/cmdline" ]; then
		tr '\0' ' ' <"/proc/$pid/cmdline" 2>/dev/null
	else
		ps -p "$pid" -o args= 2>/dev/null
	fi
}

# belongs_to_repo PID — decide whether a process looks like it belongs to this
# checkout.
#
# Port 5173 is the Vite default for every Vite project on the machine, and 8080
# is the most contested port in software. Killing whatever happens to hold a
# port would eventually take out an unrelated dev server, so scope the kill to
# processes whose command line points into this repository.
#
# Returns:
#   0 — the command line matches this repo
#   1 — the command line was readable and does NOT match
#   2 — the command line could not be read, so the caller must decide
belongs_to_repo() {
	local pid="$1" cmdline needle name

	# A binary this project owns is ours regardless of where it was built. This
	# has to come first: `go run` puts the compiled API in a temp directory, so
	# the command-line check below would wrongly call it a foreign process.
	name=$(process_name "$pid")
	for candidate in $PROJECT_BINARIES; do
		[ "$name" = "$candidate" ] && return 0
	done

	cmdline=$(process_cmdline "$pid")
	[ -n "$cmdline" ] || return 2

	# `go run` also leaves the temp binary path in its own command line, which
	# catches the toolchain wrapper process holding the port.
	case "$cmdline" in
		*overworld-ops*) return 0 ;;
	esac

	# Compare on the last two path components ("minecraftspinner/overworld-ops")
	# so the check survives the many spellings of the same path: C:\..., /c/...,
	# forward or back slashes, any case. Built with parameter expansion because
	# Git Bash ships no `rev`.
	local root_lc leaf parent
	root_lc=$(printf '%s' "$REPO_ROOT" | tr 'A-Z\\' 'a-z/')
	root_lc="${root_lc%/}"
	leaf="${root_lc##*/}"
	parent="${root_lc%/*}"
	parent="${parent##*/}"

	if [ -n "$parent" ] && [ "$parent" != "$leaf" ]; then
		needle="$parent/$leaf"
	else
		needle="$leaf"
	fi
	[ -n "$needle" ] || return 2

	if printf '%s' "$cmdline" | tr 'A-Z\\' 'a-z/' | grep -qF "$needle"; then
		return 0
	fi
	return 1
}

# kill_pid PID — terminate a process and its children. Returns non-zero if the
# process could not be killed (already gone counts as success).
kill_pid() {
	local pid="$1"

	if [ "$IS_WINDOWS" = "1" ]; then
		# //T takes the process tree: `npm run dev` spawns vite as a child, and
		# killing only the parent leaves the port held.
		taskkill //PID "$pid" //T //F >/dev/null 2>&1
	else
		kill -TERM "$pid" 2>/dev/null
		# Give it a moment to exit cleanly before insisting.
		for _ in 1 2 3 4 5; do
			kill -0 "$pid" 2>/dev/null || return 0
			sleep 0.2
		done
		kill -KILL "$pid" 2>/dev/null
	fi

	# Success if the process is gone, however it got there.
	if [ "$IS_WINDOWS" = "1" ]; then
		! tasklist //FI "PID eq $pid" //FO CSV //NH 2>/dev/null | grep -q "$pid"
	else
		! kill -0 "$pid" 2>/dev/null
	fi
}

# confirm PROMPT — ask for a y/N answer. Always false when not interactive, so
# a script in CI never blocks waiting for input that will not come.
confirm() {
	[ -t 0 ] || return 1

	local answer
	printf '%s [y/N] ' "$1"
	read -r answer
	[ "$answer" = "y" ] || [ "$answer" = "Y" ]
}
