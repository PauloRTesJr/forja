#!/bin/sh
# Serves the engine's /v1 to cores on other machines: a LAN box, a rented pod.
#
#   FORJA_ENGINE_TOKEN  required: the bearer token cores send (add it in Forja with this token)
#   FORJA_PORT          default 7861
#   FORJA_TLS_CERT/KEY  a certificate to serve; without one, a self-signed certificate is made
#                       once on the volume, and Forja pins its fingerprint when it adds the engine
#   FORJA_MODELS        models to install before serving, comma separated (or install from the app)
#   FORJA_ENGINE_ARGS   more serve flags, such as --fit-mode quality-first on a large GPU
#
# Any other command runs instead, such as `forja-engine install triposg --store /data/store`.
set -eu

if [ "$#" -gt 0 ]; then
	exec "$@"
fi

data="${FORJA_DATA_DIR:-/data}"
if [ -z "${FORJA_ENGINE_TOKEN:-}" ]; then
	echo "forja-engine: set FORJA_ENGINE_TOKEN; engines reachable from other machines need a token" >&2
	exit 2
fi

cert="${FORJA_TLS_CERT:-$data/tls/engine.crt}"
key="${FORJA_TLS_KEY:-$data/tls/engine.key}"
if [ ! -f "$cert" ]; then
	mkdir -p "$(dirname "$cert")" "$(dirname "$key")"
	(umask 077 && openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
		-subj "/CN=forja-engine" -keyout "$key" -out "$cert" 2>/dev/null)
fi
echo "forja-engine: certificate SHA-256 $(openssl x509 -in "$cert" -noout -fingerprint -sha256 | cut -d= -f2)"

if [ -n "${FORJA_MODELS:-}" ]; then
	# shellcheck disable=SC2046 # one argument per model
	forja-engine install $(echo "$FORJA_MODELS" | tr ',' ' ') --store "$data/store"
fi

# shellcheck disable=SC2086 # FORJA_ENGINE_ARGS is a list of flags
exec forja-engine serve --host 0.0.0.0 --port "${FORJA_PORT:-7861}" --store "$data/store" \
	--tls-cert "$cert" --tls-key "$key" ${FORJA_ENGINE_ARGS:-}
