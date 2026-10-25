#!/usr/bin/env bash
# End-to-end KMS compatibility check: OpenBao/Vault or a local AWS Secrets
# Manager emulator, KES, and an unmodified Silo server using KES for SSE-KMS.
#
# With KES_BASELINE set, objects are first written through that KES binary,
# which is then replaced by the one under test on the same config and key
# store; the objects must still decrypt after upgrade, rollback and restart.
# Silo is restarted at each transition to discard cached keys.
#
#   KEYSTORE      vault (default) or aws (Moto, not live AWS certification)
#   VAULT_IMAGE   key store image (default openbao/openbao:2.7.1)
#   MOTO_IMAGE    pinned Moto Secrets Manager emulator image
#   SILO_IMAGE    default pgsty/silo:RELEASE.2026-10-18T00-00-00Z
#   KES           KES binary under test (default: built from this tree)
#   KES_BASELINE  optional KES binary to write with first
#
# Requires docker, curl, jq, openssl and go. Uses ports 18200, 17373 and 19000;
# Docker Desktop on macOS uses forwarded ports instead of host networking.
set -euo pipefail
umask 077

VAULT_IMAGE=${VAULT_IMAGE:-openbao/openbao:2.7.1}
KEYSTORE=${KEYSTORE:-vault}
MOTO_IMAGE=${MOTO_IMAGE:-motoserver/moto:5.2.4@sha256:120b1589c6df7e35d2092935f6ff724486bb2c4189d0e36ce795baba1400ffd2}
SILO_IMAGE=${SILO_IMAGE:-pgsty/silo:RELEASE.2026-10-18T00-00-00Z}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
VAULT=http://127.0.0.1:18200
KES_ADDR=127.0.0.1:17373
KES_LISTEN=$KES_ADDR
SILO_KES_ENDPOINT=https://$KES_ADDR
SILO_LISTEN=127.0.0.1:19000
BACKEND_BIND=127.0.0.1
BACKEND_NETWORK=(--network host)
SILO_NETWORK=(--network host)
if [ "$(uname -s)" = Darwin ]; then
	BACKEND_NETWORK=(-p 127.0.0.1:18200:18200)
	SILO_NETWORK=(-p 127.0.0.1:19000:19000)
	BACKEND_BIND=0.0.0.0
	KES_LISTEN=0.0.0.0:17373
	SILO_LISTEN=0.0.0.0:19000
	SILO_KES_ENDPOINT=https://host.docker.internal:17373
fi
KEY=silo-default
NEW_KEY=silo-after-upgrade
NAME=kms-compat-$$
KES_PID=

cleanup() {
	local status=$?
	if [ "$status" -ne 0 ]; then
		for log in "$WORK"/kes-*.log; do [ ! -f "$log" ] || tail -80 "$log" >&2; done
		docker logs "$NAME-silo" >&2 2>/dev/null || true
	fi
	if [ -n "$KES_PID" ]; then kill "$KES_PID" 2>/dev/null || true; fi
	docker rm -f "$NAME-vault" "$NAME-aws" "$NAME-silo" >/dev/null 2>&1 || true
	if [ "${KEEP:-}" = 1 ]; then echo "kept $WORK"; else rm -r "$WORK"; fi
}
trap cleanup EXIT

wait_for() {
	for _ in $(seq 60); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done
	"$@" >&2 || true
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
# P-256 also works with macOS system curl, which cannot load Ed25519 client keys.
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 \
	-subj /CN=localhost -addext 'subjectAltName=IP:127.0.0.1,DNS:host.docker.internal' \
	-addext 'basicConstraints=critical,CA:FALSE' -addext 'extendedKeyUsage=serverAuth' \
	-keyout server.key -out server.cert >/dev/null 2>&1
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 \
	-subj /CN=silo -addext 'basicConstraints=critical,CA:FALSE' -addext 'extendedKeyUsage=clientAuth' \
	-keyout client.key -out client.cert >/dev/null 2>&1
IDENTITY=$("$KES" identity of client.cert)

cat >kes.yaml <<EOF
version: v1
address: $KES_LISTEN
admin:
  identity: $IDENTITY
tls:
  key: $WORK/server.key
  cert: $WORK/server.cert
keystore:
EOF

case "$KEYSTORE" in
vault)
	docker run -d --name "$NAME-vault" "${BACKEND_NETWORK[@]}" --cap-add IPC_LOCK "$VAULT_IMAGE" \
		server -dev -dev-root-token-id=root -dev-listen-address="$BACKEND_BIND:18200" >/dev/null
	wait_for curl -sf "$VAULT/v1/sys/health"
	vault POST sys/mounts/kv -d '{"type":"kv","options":{"version":"2"}}'
	vault POST sys/auth/approle -d '{"type":"approle"}'
	vault PUT sys/policies/acl/kes -d "$(jq -n '{policy: "path \"kv/data/kes/*\" { capabilities = [\"create\", \"read\"] }\npath \"kv/metadata/kes/*\" { capabilities = [\"list\", \"delete\"] }"}')"
	vault POST auth/approle/role/kes -d '{"token_policies":["kes"],"token_ttl":"1h"}'
	ROLE_ID=$(vault GET auth/approle/role/kes/role-id | jq -r .data.role_id)
	SECRET_ID=$(vault POST auth/approle/role/kes/secret-id | jq -r .data.secret_id)
	cat >>kes.yaml <<EOF
  vault:
    endpoint: $VAULT
    engine: kv
    version: v2
    prefix: kes
    approle:
      id: $ROLE_ID
      secret: $SECRET_ID
EOF
	;;
aws)
	docker run -d --name "$NAME-aws" "${BACKEND_NETWORK[@]}" --entrypoint moto_server \
		"$MOTO_IMAGE" -H "$BACKEND_BIND" -p 18200 >/dev/null
	wait_for curl -sf "$VAULT/moto-api/"
	cat >>kes.yaml <<EOF
  aws:
    secretsmanager:
      endpoint: $VAULT
      region: us-east-1
      credentials:
        accesskey: test
        secretkey: test
EOF
	;;
*) echo "unsupported KEYSTORE: $KEYSTORE" >&2; exit 1 ;;
esac

start_kes() {
	"$1" server --config kes.yaml >"kes-$2.log" 2>&1 &
	KES_PID=$!
	wait_for curl -sSf --max-time 5 --cacert server.cert --cert client.cert --key client.key "https://$KES_ADDR/v1/status"
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
docker run -d --name "$NAME-silo" "${SILO_NETWORK[@]}" --user "$(id -u):$(id -g)" \
	-v "$WORK:$WORK" \
	-e MINIO_ROOT_USER=compat -e MINIO_ROOT_PASSWORD=compat-password \
	-e MINIO_KMS_KES_ENDPOINT="$SILO_KES_ENDPOINT" \
	-e MINIO_KMS_KES_KEY_FILE="$WORK/client.key" \
	-e MINIO_KMS_KES_CERT_FILE="$WORK/client.cert" \
	-e MINIO_KMS_KES_CAPATH="$WORK/server.cert" \
	-e MINIO_KMS_KES_KEY_NAME="$KEY" \
	"$SILO_IMAGE" server "$WORK/silo-data" --address "$SILO_LISTEN" >/dev/null

mc() { docker exec "$NAME-silo" mcli "$@"; }

put() {
	head -c 1048576 /dev/urandom >"$1"
	mc cp -q --enc-kms "local/compat/$1=${2:-$KEY}" "$WORK/$1" "local/compat/$1" >/dev/null
}

restart_silo() {
	docker restart "$NAME-silo" >/dev/null
	wait_for mc admin kms key status local "$KEY"
}

# Inspect only this disposable backend. Never point this fixture at live AWS.
aws_fingerprint() {
	docker exec -i "$NAME-aws" python - "$KEY" "$NEW_KEY" <<'PY'
import boto3, hashlib, sys
client = boto3.client('secretsmanager', endpoint_url='http://127.0.0.1:18200',
                      region_name='us-east-1', aws_access_key_id='test', aws_secret_access_key='test')
names = sorted(s['Name'] for page in client.get_paginator('list_secrets').paginate() for s in page['SecretList'])
assert names == sorted(sys.argv[1:]), names
for name in names:
    value = client.get_secret_value(SecretId=name)['SecretString']
    print(name, hashlib.sha256(value.encode()).hexdigest())
PY
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
	restart_silo
fi

check before.bin
mc admin kms key create local "$NEW_KEY" >/dev/null
put after.bin "$NEW_KEY"
check after.bin

if [ -n "${KES_BASELINE:-}" ]; then
	stop_kes
	start_kes "$KES_BASELINE" rollback
	restart_silo
	check before.bin
	check after.bin
	echo "ok: rollback reads keys and objects created by both versions"
fi

stop_kes
start_kes "$KES" restart
restart_silo
check before.bin
check after.bin

if [ "$KEYSTORE" = aws ]; then
	before=$(aws_fingerprint)
	(cd "$ROOT" && go test ./kesconf -run '^TestAWS$' -count=1 -v -aws.config="$WORK/kes.yaml")
	after=$(aws_fingerprint)
	[ "$before" = "$after" ]
	echo "ok: Secrets Manager key contents survived upgrades, rollback and test cleanup"
else
	for key in "$KEY" "$NEW_KEY"; do
		vault GET "kv/data/kes/$key" | jq -e '.data.data | length > 0' >/dev/null
	done
	echo "ok: both keys are stored in $VAULT_IMAGE under kv/kes"
fi
