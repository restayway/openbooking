#!/usr/bin/env bash
set -euo pipefail

PDS_URL="${PDS_URL:-http://127.0.0.1:8080}"
RELAY_URL="${RELAY_URL:-http://127.0.0.1:8081}"
APPVIEW_URL="${APPVIEW_URL:-http://127.0.0.1:8082}"
PROVIDER_DID="${PROVIDER_DID:-did:web:provider.example}"

curl -fsS "$PDS_URL/readyz" >/dev/null
curl -fsS "$RELAY_URL/readyz" >/dev/null
curl -fsS "$APPVIEW_URL/readyz" >/dev/null
curl -fsS "$RELAY_URL/v1/status"
curl -fsS "$APPVIEW_URL/v1/providers/$PROVIDER_DID/listings"
