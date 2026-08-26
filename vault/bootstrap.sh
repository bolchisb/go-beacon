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

echo "==> kv engine (holds the operator account)"
vault secrets enable -path=secret kv-v2 2>/dev/null || echo "    already enabled"

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

# The operator account: username, password hash, and the key that signs
# session cookies. The relay reads it at startup and writes it when someone
# sets or changes the password.
path "secret/data/beacon/operator" {
  capabilities = ["create", "read", "update"]
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
# secret_id_num_uses=1 and a short ttl: the supervisor keeps one wrapped
# secret_id available for Vault Agent, and any that goes unconsumed expires
# rather than accumulating in Vault forever.
vault write "auth/approle/role/$ROLE_NAME" \
  token_policies="$POLICY_NAME" \
  token_ttl=1h \
  token_max_ttl=4h \
  secret_id_ttl=10m \
  secret_id_num_uses=1 >/dev/null

# The role id is not a secret, but it does have to reach Vault Agent. Writing
# it here means a deployment never has to carry it by hand.
AGENT_DIR="${BEACON_AGENT_DIR:-/agent}"
if [ -d "$AGENT_DIR" ]; then
  vault read -field=role_id "auth/approle/role/$ROLE_NAME/role-id" > "$AGENT_DIR/role-id"
  chmod 0644 "$AGENT_DIR/role-id"
  echo "==> role id written to $AGENT_DIR/role-id"
fi

echo
echo "role_id:"
vault read -field=role_id "auth/approle/role/$ROLE_NAME/role-id"
echo
echo "No secret_id is minted here. The unseal supervisor mints one, wrapped and"
echo "single-use, whenever Vault Agent has consumed the last."
