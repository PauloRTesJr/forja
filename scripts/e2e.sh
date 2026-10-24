#!/usr/bin/env bash
# End-to-end runs of the built forja CLI, isolated from any installed app: its own data folder,
# config folder and core socket, and this checkout's engine.
#
#   scripts/e2e.sh generate [image...]   generate each image (default: the engine's chest fixture),
#                                        save the GLBs into $FORJA_E2E_OUT/glb and check each one
#   scripts/e2e.sh forja <args...>       run any forja command in the same environment
#
# FORJA_E2E_MODELS  stubs (default): deterministic CPU stand-ins; real: models installed in the data folder
# FORJA_E2E_DATA    data folder (default: $FORJA_E2E_OUT/data); keep one to reuse installed models
# FORJA_E2E_OUT     outputs (default: a new temporary folder)
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

# Paths the native forja binary reads; on Windows (Git Bash) they must be Windows paths.
native() {
	if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

exe=""
case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) exe=".exe" ;; esac
forja="$root/core/bin/forja$exe"
[ -x "$forja" ] || {
	echo "e2e: $forja is missing; run make core first" >&2
	exit 1
}

out=${FORJA_E2E_OUT:-$(mktemp -d)}
data=${FORJA_E2E_DATA:-$out/data}
if command -v cygpath >/dev/null 2>&1; then
	out=$(cygpath -u "$out") # runners hand out D:\a\_temp-style paths
	data=$(cygpath -u "$data")
fi
models=${FORJA_E2E_MODELS:-stubs}
mkdir -p "$out" "$data/config"

export FORJA_DATA_DIR FORJA_CONFIG_DIR
FORJA_DATA_DIR=$(native "$data")
FORJA_CONFIG_DIR=$(native "$data/config")
if [ -n "$exe" ]; then
	# Windows defaults to one pipe per user, which an installed app may already serve.
	FORJA_SOCKET='\\.\pipe\forja-e2e-'$(printf '%s' "$FORJA_DATA_DIR" | cksum | cut -d' ' -f1)
	export FORJA_SOCKET
fi

engine=$(native "$root/engine")
{
	echo "# Written by scripts/e2e.sh."
	echo "[engine]"
	echo "command = [\"uv\", \"run\", \"--quiet\", \"--project\", \"$engine\", \"forja-engine\"]"
	# A cold runner imports numpy, scipy and trimesh for the first time.
	echo "start_timeout = \"300s\""
	if [ "$models" = stubs ]; then
		echo "[models]"
		echo "stubs = true"
	fi
} >"$data/config/config.toml"

logs() {
	for f in "$data"/logs/*.log; do
		[ -f "$f" ] || continue
		echo "::group::$f"
		tail -n 200 "$f"
		echo "::endgroup::"
	done
}

generate() {
	[ $# -gt 0 ] || set -- "$root/engine/tests/fixtures/chest.png"
	local images=() i
	for i in "$@"; do images+=("$(native "$i")"); done
	if ! "$forja" generate --json "${images[@]}" >"$out/jobs.json"; then
		cat "$out/jobs.json" >&2 || true
		logs >&2
		return 1
	fi
	local ids
	ids=$(grep -o '"asset_id": *"[^"]*"' "$out/jobs.json" | sed 's/.*"\([^"]*\)"$/\1/')
	local n
	n=$(printf '%s\n' "$ids" | grep -c . || true)
	if [ "$n" -ne $# ]; then
		echo "e2e: $# images gave $n assets" >&2
		cat "$out/jobs.json" >&2
		return 1
	fi
	mkdir -p "$out/glb"
	# shellcheck disable=SC2086 # one asset id per word
	"$forja" assets get $ids --output "$(native "$out/glb")"
	local id glb
	for id in $ids; do
		"$forja" assets show "$id" --json >"$out/asset-$id.json"
		grep -q '"triangles": *[1-9]' "$out/asset-$id.json" || {
			echo "e2e: asset $id has no triangles" >&2
			cat "$out/asset-$id.json" >&2
			return 1
		}
	done
	for glb in "$out"/glb/*.glb; do
		[ "$(head -c 4 "$glb")" = glTF ] || {
			echo "e2e: $glb is not a GLB" >&2
			return 1
		}
		echo "e2e: ok $(basename "$glb") ($(wc -c <"$glb" | tr -d ' ') bytes)"
	done
}

cmd=${1:-generate}
[ $# -gt 0 ] && shift
case "$cmd" in
generate) generate "$@" ;;
forja) "$forja" "$@" ;;
*)
	echo "usage: scripts/e2e.sh generate [image...] | forja <args...>" >&2
	exit 2
	;;
esac
