#!/usr/bin/env bash
# End-to-end KMS compatibility check: a Vault-API key store (OpenBao or
# HashiCorp Vault), the KES server built from this tree, and an unmodified
# Silo server using KES for SSE-KMS.
#
# With KES_BASELINE set, objects are first written through that KES binary,
# which is then replaced by the one under test on the same config and key
# store; the objects must still decrypt. This is the upgrade path from the
# last upstream release.
#
#   VAULT_IMAGE   key store image (default openbao/openbao:2.7.1)
#   SILO_IMAGE    default pgsty/silo:RELEASE.2026-09-16T00-00-00Z
#   KES           KES binary under test (default: built from this tree)
#   KES_BASELINE  optional KES binary to write with first
#
# Requires docker, curl, jq and go. Uses the host network on ports 18200,
# 17373 and 19000.
set -euo pipefail
umask 077

VAULT_IMAGE=${VAULT_IMAGE:-openbao/openbao:2.7.1}
SILO_IMAGE=${SILO_IMAGE:-pgsty/silo:RELEASE.2026-09-16T00-00-00Z}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
VAULT=http://127.0.0.1:18200
KES_ADDR=127.0.0.1:17373
KEY=silo-default
NAME=kms-compat-$$
KES_PID=

cleanup() {
	local status=$?
	if [ "$status" -ne 0 ]; then
		for log in "$WORK"/kes-*.log; do [ ! -f "$log" ] || tail -80 "$log" >&2; done
		docker logs "$NAME-silo" >&2 2>/dev/null || true
	fi
	if [ -n "$KES_PID" ]; then kill "$KES_PID" 2>/dev/null || true; fi
	docker rm -f "$NAME-vault" "$NAME-silo" >/dev/null 2>&1 || true
	if [ "${KEEP:-}" = 1 ]; then echo "kept $WORK"; else rm -r "$WORK"; fi
}
trap cleanup EXIT

wait_for() {
	for _ in $(seq 60); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done
	echo "timed out waiting for: $*" >&2
	return 1
}

vault() {
	local method=$1 path=$2
	shift 2
	curl -sSf -X "$method" -H "X-Vault-Token: root" "$VAULT/v1/$path" "$@"
}

if [ -z "${KES:-}" ]; then
	(cd "$ROOT" && CGO_ENABLED=0 go build -o "$WORK/kes" ./cmd/kes)
	KES=$WORK/kes
fi
"$KES" --version

cd "$WORK"
"$KES" identity new --ip 127.0.0.1 --key server.key --cert server.cert localhost >/dev/null
"$KES" identity new --key client.key --cert client.cert silo >/dev/null
IDENTITY=$("$KES" identity of client.cert)

docker run -d --name "$NAME-vault" --network host --cap-add IPC_LOCK "$VAULT_IMAGE" \
	server -dev -dev-root-token-id=root -dev-listen-address=127.0.0.1:18200 >/dev/null
wait_for curl -sf "$VAULT/v1/sys/health"
vault POST sys/mounts/kv -d '{"type":"kv","options":{"version":"2"}}'
vault POST sys/auth/approle -d '{"type":"approle"}'
vault PUT sys/policies/acl/kes -d "$(jq -n '{policy: "path \"kv/data/kes/*\" { capabilities = [\"create\", \"read\"] }\npath \"kv/metadata/kes/*\" { capabilities = [\"list\", \"delete\"] }"}')"
vault POST auth/approle/role/kes -d '{"token_policies":["kes"],"token_ttl":"1h"}'
ROLE_ID=$(vault GET auth/approle/role/kes/role-id | jq -r .data.role_id)
SECRET_ID=$(vault POST auth/approle/role/kes/secret-id | jq -r .data.secret_id)

cat >kes.yaml <<EOF
version: v1
address: $KES_ADDR
admin:
  identity: $IDENTITY
tls:
  key: $WORK/server.key
  cert: $WORK/server.cert
keystore:
  vault:
    endpoint: $VAULT
    engine: kv
    version: v2
    prefix: kes
    approle:
      id: $ROLE_ID
      secret: $SECRET_ID
EOF

start_kes() {
	"$1" server --config kes.yaml >"kes-$2.log" 2>&1 &
	KES_PID=$!
	wait_for curl -sf --cacert server.cert --cert client.cert --key client.key "https://$KES_ADDR/v1/status"
}

stop_kes() {
	kill "$KES_PID"
	wait "$KES_PID" 2>/dev/null || true
	KES_PID=
}

if [ -n "${KES_BASELINE:-}" ]; then
	"$KES_BASELINE" --version
	start_kes "$KES_BASELINE" baseline
else
	start_kes "$KES" test
fi


mkdir silo-data
docker run -d --name "$NAME-silo" --network host --user "$(id -u):$(id -g)" \
	-v "$WORK:$WORK" \
	-e MINIO_ROOT_USER=compat -e MINIO_ROOT_PASSWORD=compat-password \
	-e MINIO_KMS_KES_ENDPOINT="https://$KES_ADDR" \
	-e MINIO_KMS_KES_KEY_FILE="$WORK/client.key" \
	-e MINIO_KMS_KES_CERT_FILE="$WORK/client.cert" \
	-e MINIO_KMS_KES_CAPATH="$WORK/server.cert" \
	-e MINIO_KMS_KES_KEY_NAME="$KEY" \
	"$SILO_IMAGE" server "$WORK/silo-data" --address 127.0.0.1:19000 >/dev/null

mc() { docker exec "$NAME-silo" mcli "$@"; }

put() {
	head -c 1048576 /dev/urandom >"$1"
	mc cp -q --enc-kms "local/compat/$1=$KEY" "$WORK/$1" "local/compat/$1" >/dev/null
}

check() {
	mc stat --json "local/compat/$1" | jq -e '.metadata["X-Amz-Server-Side-Encryption"] == "aws:kms"' >/dev/null
	cmp <(mc cat "local/compat/$1") "$1"
	echo "ok: $1 is SSE-KMS and decrypts"
}


wait_for mc alias set local http://127.0.0.1:19000 compat compat-password
wait_for mc admin kms key status local "$KEY"
mc mb -q local/compat >/dev/null
put before.bin
check before.bin

if [ -n "${KES_BASELINE:-}" ]; then
	stop_kes
	start_kes "$KES" test
	wait_for mc admin kms key status local "$KEY"
fi

check before.bin
put after.bin
check after.bin
vault GET "kv/data/kes/$KEY" | jq -e '.data.data | length > 0' >/dev/null
echo "ok: $KEY is stored in $VAULT_IMAGE under kv/kes"
