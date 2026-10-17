#!/usr/bin/env bash
# Copyright 2026 - PGSTY contributors.
# Licensed under the GNU AGPL v3.0; see LICENSE.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

require_text() {
    if ! grep -F -- "$2" "$1" > /dev/null; then
        echo "Missing branding or compatibility marker in $1: $2" >&2
        exit 1
    fi
}

# Attribution and compatibility identifiers are intentional. Marketing,
# support and download links must not send users to upstream services.
pattern='https?://([[:alnum:]-]+[.])*min[.]io|[[:alnum:]_.+-]+@min[.]io|MinIO (SUBNET|Community Support|KES is)|quay[.]io/minio|github[.]com/minio/kes/(releases|wiki|blob/master)|assignees: aead'
if matches=$(git grep -n -I -E "$pattern" -- . ':!CREDITS' ':!NOTICE' ':!buildscripts/verify-branding.sh'); then
    printf 'Unexpected upstream branding or service links:\n%s\n' "$matches" >&2
    exit 1
else
    status=$?
    [ "$status" -eq 1 ] || exit "$status"
fi

for file in LICENSE NOTICE CREDITS; do
    test -s "$file"
done
for file in .github/logo.svg .github/arch.png .github/grafana-dashboard.png; do
    if [ -e "$file" ]; then
        echo "Retired upstream artwork has returned: $file" >&2
        exit 1
    fi
done

require_text README.md '# PGSTY KES'
require_text NOTICE 'MinIO KES, Copyright (C) 2015-2025 MinIO, Inc.'
require_text NOTICE 'PGSTY KES modifications, Copyright (C) 2026 PGSTY contributors.'
require_text NOTICE 'It is not affiliated with, endorsed by, or sponsored by MinIO, Inc.'
require_text cmd/kes/brand.go 'productName = "PGSTY KES"'
require_text cmd/kes/brand.go '2015-2025 MinIO, Inc.'
require_text cmd/kes/brand.go '2026 PGSTY contributors'
require_text cmd/kes/main.go 'upstreamCopyright'
require_text cmd/kes/server.go 'upstreamCopyright'
require_text Dockerfile 'org.opencontainers.image.title="PGSTY KES"'
require_text Dockerfile 'org.opencontainers.image.licenses="AGPL-3.0-only"'
require_text .github/nfpm.yml 'description: PGSTY KES key encryption service.'
require_text .github/nfpm.yml 'license: AGPL-3.0-only'
require_text cmd/kes/update.go 'PGSTY KES does not self-update'

# Renaming these can break existing clients, configuration or stored keys.
require_text go.mod 'module github.com/minio/kes'
require_text internal/protobuf/crypto.proto 'package miniohq.kms;'
for name in MINIO_KES_SERVER KES_SERVER MINIO_KMS_KES_ENDPOINT \
            MINIO_KES_API_KEY KES_API_KEY MINIO_KMS_KES_API_KEY \
            MINIO_KES_KEY_FILE KES_CLIENT_KEY MINIO_KES_CLIENT_KEY \
            MINIO_KES_CERT_FILE KES_CLIENT_CERT MINIO_KES_CLIENT_CERT; do
    require_text internal/cli/env.go "\"${name}\""
done

echo 'PGSTY KES branding, attribution and compatibility markers verified.'
