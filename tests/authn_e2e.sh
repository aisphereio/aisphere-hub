#!/usr/bin/env bash
#
# aisphere-hub authn E2E flow verification.
#
# This script walks through the full OAuth code flow:
#   1. Get login URL
#   2. (Manual step) Open the URL in a browser, log in to Casdoor, copy
#      the code & state from the callback URL.
#   3. Exchange code for tokens
#   4. Call /me with the access token
#   5. Call /introspect to verify token validity
#   6. (Optional) Refresh the token
#   7. (Optional) Revoke the token and verify /me returns 401
#
# Usage:
#   ./authn_e2e.sh                          # Interactive: prompts for code/state
#   ./authn_e2e.sh --code CODE --state ST   # Non-interactive
#   HUB=http://127.0.0.1:8000 ./authn_e2e.sh  # Override hub base URL
#
# Prerequisites:
#   - hub is running on $HUB (default http://127.0.0.1:8000)
#   - casdoor is running and reachable from both hub and your browser
#   - casdoor application has redirect_uri = http://localhost:3000/callback
#     (or whatever you pass via --redirect-uri)
#   - jq is installed (for pretty-printing JSON responses)
#
# Environment:
#   HUB           hub base URL              (default: http://127.0.0.1:8000)
#   REDIRECT_URI  callback URL              (default: http://localhost:3000/callback)
#   SCOPE         OAuth scope               (default: read)

set -euo pipefail

HUB="${HUB:-http://127.0.0.1:8000}"
REDIRECT_URI="${REDIRECT_URI:-http://localhost:3000/callback}"
SCOPE="${SCOPE:-read}"
STATE="${STATE:-aisphere-hub}"

# Colors for output
G() { printf '\033[32m%s\033[0m\n' "$*"; }
Y() { printf '\033[33m%s\033[0m\n' "$*"; }
B() { printf '\033[34m%s\033[0m\n' "$*"; }
R() { printf '\033[31m%s\033[0m\n' "$*"; }
D() { printf '\033[90m%s\033[0m\n' "$*"; }

# Parse args
CODE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --code) CODE="$2"; shift 2 ;;
    --state) STATE="$2"; shift 2 ;;
    --redirect-uri) REDIRECT_URI="$2"; shift 2 ;;
    --hub) HUB="$2"; shift 2 ;;
    *) R "Unknown arg: $1"; exit 1 ;;
  esac
done

D "Hub: $HUB"
D "Redirect URI: $REDIRECT_URI"
D "Scope: $SCOPE"
D "State: $STATE"
echo

# ------------------------------------------------------------------------------
B "Step 1: GET /v1/authn/login-url"
D "Request:"
D "  GET $HUB/v1/authn/login-url?redirect_uri=$(printf %s "$REDIRECT_URI" | jq -sRr @uri)&scope=$SCOPE&state=$STATE"
echo

LOGIN_URL_RESP=$(curl -sS \
  -G "$HUB/v1/authn/login-url" \
  --data-urlencode "redirect_uri=$REDIRECT_URI" \
  --data-urlencode "scope=$SCOPE" \
  --data-urlencode "state=$STATE")

G "Response:"
echo "$LOGIN_URL_RESP" | jq .
LOGIN_URL=$(echo "$LOGIN_URL_RESP" | jq -r .login_url)
echo

if [[ -z "$CODE" ]]; then
  Y "Step 2: MANUAL — Open this URL in a browser and log in:"
  Y ""
  Y "  $LOGIN_URL"
  Y ""
  Y "After logging in, Casdoor will redirect to:"
  Y "  $REDIRECT_URI?code=XXXX&state=$STATE"
  Y ""
  Y "Copy the 'code' query parameter from the redirected URL and paste it below."
  echo
  read -r -p "Paste code here: " CODE
  if [[ -z "$CODE" ]]; then
    R "No code provided, aborting."
    exit 1
  fi
fi

# ------------------------------------------------------------------------------
echo
B "Step 3: POST /v1/authn/exchange"
D "Request body:"
D "  {"
D "    \"code\": \"$CODE\","
D "    \"redirect_uri\": \"$REDIRECT_URI\","
D "    \"state\": \"$STATE\""
D "  }"
echo

EXCHANGE_RESP=$(curl -sS \
  -X POST "$HUB/v1/authn/exchange" \
  -H "Content-Type: application/json" \
  -d "$(jq -nc --arg code "$CODE" --arg ruri "$REDIRECT_URI" --arg state "$STATE" \
        '{code: $code, redirect_uri: $ruri, state: $state}')")

G "Response:"
echo "$EXCHANGE_RESP" | jq .

ACCESS_TOKEN=$(echo "$EXCHANGE_RESP" | jq -r .access_token)
REFRESH_TOKEN=$(echo "$EXCHANGE_RESP" | jq -r .refresh_token)
ID_TOKEN=$(echo "$EXCHANGE_RESP" | jq -r .id_token)
EXPIRES_IN=$(echo "$EXCHANGE_RESP" | jq -r .expires_in)

if [[ -z "$ACCESS_TOKEN" || "$ACCESS_TOKEN" == "null" ]]; then
  R "Exchange failed: no access_token in response"
  exit 1
fi

D ""
D "Extracted:"
D "  access_token:  ${ACCESS_TOKEN:0:40}... (truncated)"
D "  refresh_token: ${REFRESH_TOKEN:0:40}... (truncated)"
D "  id_token:      ${ID_TOKEN:0:40}... (truncated)"
D "  expires_in:    ${EXPIRES_IN}s"
echo

# ------------------------------------------------------------------------------
B "Step 4: GET /v1/authn/me (with Bearer token)"
D "Request:"
D "  GET $HUB/v1/authn/me"
D "  Authorization: Bearer ${ACCESS_TOKEN:0:40}..."
echo

ME_RESP=$(curl -sS \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  "$HUB/v1/authn/me")

G "Response:"
echo "$ME_RESP" | jq .
echo

# ------------------------------------------------------------------------------
B "Step 5: POST /v1/authn/introspect (verify token validity)"
D "Request body:"
D "  {\"token\": \"<access_token>\", \"token_type\": \"access_token\"}"
echo

INTROSPECT_RESP=$(curl -sS \
  -X POST "$HUB/v1/authn/introspect" \
  -H "Content-Type: application/json" \
  -d "$(jq -nc --arg t "$ACCESS_TOKEN" '{token: $t, token_type: "access_token"}')")

G "Response:"
echo "$INTROSPECT_RESP" | jq .
echo

# ------------------------------------------------------------------------------
B "Step 6: POST /v1/authn/refresh (optional — refresh the access token)"
Y "Press Enter to refresh the token, or Ctrl+C to skip..."
read -r

REFRESH_RESP=$(curl -sS \
  -X POST "$HUB/v1/authn/refresh" \
  -H "Content-Type: application/json" \
  -d "$(jq -nc --arg rt "$REFRESH_TOKEN" '{refresh_token: $rt}')")

G "Response:"
echo "$REFRESH_RESP" | jq .

NEW_ACCESS_TOKEN=$(echo "$REFRESH_RESP" | jq -r .access_token)
if [[ -n "$NEW_ACCESS_TOKEN" && "$NEW_ACCESS_TOKEN" != "null" ]]; then
  D "New access token: ${NEW_ACCESS_TOKEN:0:40}... (truncated)"
  ACCESS_TOKEN="$NEW_ACCESS_TOKEN"
fi
echo

# ------------------------------------------------------------------------------
B "Step 7: POST /v1/authn/revoke (optional — revoke and verify /me returns 401)"
Y "Press Enter to revoke the token and verify rejection, or Ctrl+C to skip..."
read -r

REVOKE_RESP=$(curl -sS \
  -X POST "$HUB/v1/authn/revoke" \
  -H "Content-Type: application/json" \
  -d "$(jq -nc --arg t "$ACCESS_TOKEN" '{token: $t, token_type: "access_token"}')")

G "Revoke response:"
echo "$REVOKE_RESP" | jq .
echo

B "Step 8: GET /v1/authn/me again (should now fail with 401)"
D "Request:"
D "  GET $HUB/v1/authn/me"
D "  Authorization: Bearer ${ACCESS_TOKEN:0:40}..."
echo

ME_AFTER_REVOKE=$(curl -sS -o /dev/null -w "%{http_code}" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  "$HUB/v1/authn/me" || true)

if [[ "$ME_AFTER_REVOKE" == "401" ]]; then
  G "✓ Revocation works: /me returned 401 after revoke"
elif [[ "$ME_AFTER_REVOKE" == "200" ]]; then
  R "✗ Revocation FAILED: /me still returned 200 after revoke"
  R "  This likely means the kernel CachedClient cache was not invalidated."
  R "  Check hub/internal/data/authn.go:RevokeLocal calls CachedTokenService.Invalidate."
else
  Y "? Unexpected status: $ME_AFTER_REVOKE"
fi
echo

G "================================================================"
G "E2E flow complete."
G "================================================================"
