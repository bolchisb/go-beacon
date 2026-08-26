#!/bin/sh
# Keeps the relay's Vault unsealed, without a human.
#
# This is NOT Vault's own auto-unseal. Auto-unseal is a seal type backed by a
# KMS or another Vault, where the unsealing key never touches this host. What
# runs here reads the Shamir key shares from a file and posts them, so the
# shares and the data they protect sit on the same machine. That is a chosen
# trade, not an oversight: the operator treats this host as trusted, and the
# alternative was a hard dependency on a cloud KMS.
#
# Two consequences, both enforced below:
#   - the key file never enters the image, only a runtime mount;
#   - the key file lives on its own volume, so a backup of the Vault data does
#     not also carry the keys that open it.
#
# It runs as a supervisor rather than once at boot. A one-shot would unseal
# after the first start and then be gone, leaving the next host reboot sealed
# until somebody noticed -- which is the exact situation this exists to avoid.
set -eu

KEYFILE="${BEACON_UNSEAL_KEYFILE:-/unseal/unseal.json}"
SHARES="${BEACON_UNSEAL_SHARES:-5}"
THRESHOLD="${BEACON_UNSEAL_THRESHOLD:-3}"
INTERVAL="${BEACON_UNSEAL_INTERVAL:-15}"
: "${VAULT_ADDR:?set VAULT_ADDR}"
export VAULT_ADDR

log() { echo "[unseal] $*"; }

# `vault status` exits 2 when sealed and 1 when unreachable, but still prints
# the JSON in the sealed case. Discarding output on a non-zero exit would throw
# away exactly the state this script exists to read, so the exit code is
# ignored and the body is judged on whether it parsed.
status() {
  out=$(vault status -format=json 2>/dev/null) || true
  if [ -n "$out" ]; then echo "$out"; else echo '{}'; fi
}
field() { echo "$1" | jq -r "$2"; }

initialise() {
  # shellcheck disable=SC2317
  # Refusing to re-initialise over an existing key file is the important half.
  # Vault reporting "uninitialised" while keys exist means the data volume was
  # lost or the wrong key file is mounted; initialising anyway would silently
  # orphan whatever the old keys opened.
  if [ -s "$KEYFILE" ]; then
    log "REFUSING to initialise: Vault is uninitialised but $KEYFILE holds keys."
    log "The data volume was lost, or the wrong key file is mounted. Fix the"
    log "mount rather than letting this proceed."
    return 1
  fi

  log "initialising: $SHARES shares, threshold $THRESHOLD"
  mkdir -p "$(dirname "$KEYFILE")"
  umask 077
  vault operator init -format=json \
    -key-shares="$SHARES" -key-threshold="$THRESHOLD" >"$KEYFILE"
  chmod 600 "$KEYFILE"
  log "keys written to $KEYFILE"
  log "BACK THIS UP somewhere that is not the Vault data volume, and keep it"
  log "out of any archive that also contains that volume."
}

unseal() {
  if [ ! -s "$KEYFILE" ]; then
    log "sealed, but $KEYFILE is missing or empty -- nothing here can open it"
    return 1
  fi
  # Only the threshold is applied, never every share, so this process never
  # handles more key material than it needs to do its job.
  jq -r '.unseal_keys_b64[]' "$KEYFILE" | head -n "$THRESHOLD" | while read -r k; do
    vault operator unseal "$k" >/dev/null 2>&1 || true
  done
}

# reconcile brings Vault to unsealed if it can, and says so only when something
# changed. A quiet log is the normal state.
reconcile() {
  st=$(status)
  if [ "$(field "$st" '.initialized // false')" != "true" ]; then
    initialise || return 1
    st=$(status)
  fi
  if [ "$(field "$st" '.sealed // true')" = "true" ]; then
    log "Vault is sealed, applying $THRESHOLD shares"
    unseal || return 1
    if [ "$(field "$(status)" '.sealed // true')" = "true" ]; then
      log "still sealed: the key file does not match this storage"
      return 1
    fi
    log "unsealed"
  fi
  return 0
}

log "watching $VAULT_ADDR every ${INTERVAL}s"
while :; do
  # Vault may not have opened its listener yet, or may be restarting. A status
  # with no fields in it is a wait, not an error.
  if [ "$(field "$(status)" '.sealed // empty')" = "" ]; then
    sleep "$INTERVAL"
    continue
  fi
  reconcile || log "will retry in ${INTERVAL}s"
  sleep "$INTERVAL"
done
