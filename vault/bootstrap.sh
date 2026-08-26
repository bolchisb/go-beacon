#!/bin/sh
# Prepare a Vault for the beacon relay: one transit key for signing agent
# assertions, and a policy that lets the relay sign with it and read its public
# key -- and do nothing else.
#
# Run once per deployment, against an unsealed Vault, with VAULT_ADDR and a
# VAULT_TOKEN that can write policies:
#
#   VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=... ./vault/bootstrap.sh
#
# It is idempotent: re-running it does not rotate the key or invalidate any
# credential already issued.
set -eu

KEY_NAME="${BEACON_TRANSIT_KEY:-beacon-agent-assertions}"
POLICY_NAME="beacon-relay"
ROLE_NAME="beacon-relay"

: "${VAULT_ADDR:?set VAULT_ADDR}"
: "${VAULT_TOKEN:?set VAULT_TOKEN}"
export VAULT_ADDR VAULT_TOKEN

echo "==> transit engine"
vault secrets enable transit 2>/dev/null || echo "    already enabled"

echo "==> signing key: $KEY_NAME"
# ed25519: small signatures, fast verification, and no parameter choices to get
# wrong. exportable stays false -- the relay never needs the private half.
vault write -f "transit/keys/$KEY_NAME" type=ed25519 2>/dev/null \
  || echo "    already exists"

echo "==> policy: $POLICY_NAME"
vault policy write "$POLICY_NAME" - <<EOF
# Sign an agent assertion. This is the relay's whole reason to hold a token.
path "transit/sign/$KEY_NAME" {
  capabilities = ["update"]
}

# Read the public half so the relay can verify locally and keep working while
# Vault is sealed or unreachable.
path "transit/export/public-key/$KEY_NAME" {
  capabilities = ["read"]
}
path "transit/keys/$KEY_NAME" {
  capabilities = ["read"]
}

# Mint the response-wrapped, single-use enrolment tokens handed to a new agent.
path "sys/wrapping/wrap" {
  capabilities = ["update"]
}
path "sys/wrapping/unwrap" {
  capabilities = ["update"]
}
EOF

echo "==> approle for the relay"
vault auth enable approle 2>/dev/null || echo "    already enabled"
vault write "auth/approle/role/$ROLE_NAME" \
  token_policies="$POLICY_NAME" \
  token_ttl=1h \
  token_max_ttl=4h \
  secret_id_ttl=0 \
  secret_id_num_uses=0 >/dev/null

echo
echo "role_id:"
vault read -field=role_id "auth/approle/role/$ROLE_NAME/role-id"
echo
echo "A secret_id is deliberately not minted here. Create one when you deploy:"
echo "  vault write -f -wrap-ttl=5m auth/approle/role/$ROLE_NAME/secret-id"
echo "and hand the wrapping token to the relay, not the secret_id itself."
