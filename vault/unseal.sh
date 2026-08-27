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
AGENTDIR="${BEACON_AGENT_DIR:-/agent}"
AGENT_ROLE="${BEACON_AGENT_ROLE:-beacon-relay}"
WRAP_TTL="${BEACON_WRAP_TTL:-5m}"
# Replace the wrapped secret id once it gets this old. Must be under the wrap
# ttl above, or Agent can find one that has already expired.
WRAP_REFRESH_MIN="${BEACON_WRAP_REFRESH_MIN:-4}"
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
# Reads a boolean out of a status document.
#
# Not `jq -r '.sealed // true'`: jq's // operator treats false as absent, so
# that expression returns "true" for an unsealed Vault -- which silently
# inverted the one value this whole script turns on. Booleans are read
# explicitly, and a genuinely missing field comes back empty.
boolfield() {
  echo "$1" | jq -r "if has(\"$2\") then (.$2|tostring) else \"\" end" 2>/dev/null || echo ""
}

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
  # Checked rather than trusted: this function is called as `initialise || ...`,
  # which switches set -e off for everything inside it. An unwritable $KEYFILE
  # -- a volume the container's user does not own -- otherwise failed here and
  # still reported success two lines down, sending the reader after the wrong
  # problem entirely.
  if ! vault operator init -format=json \
      -key-shares="$SHARES" -key-threshold="$THRESHOLD" >"$KEYFILE"; then
    log "initialisation failed, or $KEYFILE could not be written."
    log "The volume holding it must be writable by uid $(id -u)."
    return 1
  fi
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
  # One share at a time, checking after each, rather than firing the threshold
  # blindly. Right after an init Vault can still reject a share, and a silent
  # retry loop would leave progress stuck below the threshold with nothing in
  # the log to say why. Stopping as soon as it opens also means this process
  # never handles more key material than the job needed.
  n=0
  errfile=$(mktemp)
  # shellcheck disable=SC2064
  trap "rm -f '$errfile'" EXIT
  for k in $(jq -r '.unseal_keys_b64[]' "$KEYFILE"); do
    n=$((n + 1))
    # The unseal call returns the resulting seal status itself. Reading that,
    # rather than asking again afterwards, avoids racing the state change --
    # an earlier version reported failure for an unseal that had just
    # succeeded, which is worse than a real failure because the log lies.
    # stderr is kept out of $out on purpose. Vault writes warnings there, and
    # merging them into the JSON makes jq fail silently -- which read as "still
    # sealed" for a share that had in fact just opened it.
    if out=$(vault operator unseal -format=json "$k" 2>"$errfile"); then
      if [ "$(boolfield "$out" sealed)" = "false" ]; then
        return 0
      fi
    else
      log "share $n was rejected: $(head -1 "$errfile" 2>/dev/null)"
    fi
  done
  return 1
}

# ensure_agent_credentials keeps one response-wrapped, single-use secret_id
# waiting for Vault Agent.
#
# Agent deletes the file once it has unwrapped it, so its absence is exactly the
# signal that a new one is needed -- on first boot, and again whenever Agent has
# had to re-authenticate. Anything minted and not consumed expires on its own,
# because the role issues secret_ids with a short ttl and a single use.
#
# This is the piece that makes the wrapped-secret-id pattern work on plain
# Docker at all. On Kubernetes the Vault injector delivers it; here nothing
# would, except that this process already holds the root token and already runs
# at every boot.
ensure_agent_credentials() {
  [ -d "$AGENTDIR" ] || return 0
  [ -s "$KEYFILE" ] || return 0

  wrapfile="$AGENTDIR/wrapped-secret-id"
  if [ -s "$wrapfile" ]; then
    # A wrapping token expires whether or not anyone used it. Agent only
    # re-authenticates when its own token runs out of renewals -- hours later --
    # and would otherwise find a token that lapsed minutes after it was minted,
    # with nothing replacing it because the file was still there. Staleness, not
    # absence, is the condition that matters.
    if [ -z "$(find "$wrapfile" -mmin "+$WRAP_REFRESH_MIN" 2>/dev/null)" ]; then
      return 0
    fi
    log "the wrapped secret id has gone stale, replacing it"
  fi

  root=$(jq -r '.root_token // empty' "$KEYFILE" 2>/dev/null)
  [ -n "$root" ] || return 0

  # Quietly do nothing until bootstrap has created the role. A fresh deployment
  # runs the supervisor before anyone has run vault-init, and that is normal
  # rather than an error worth logging every 15 seconds.
  if ! VAULT_TOKEN="$root" vault read "auth/approle/role/$AGENT_ROLE/role-id" \
      >/dev/null 2>&1; then
    return 0
  fi

  wrapped=$(VAULT_TOKEN="$root" vault write -f -format=json \
    -wrap-ttl="$WRAP_TTL" "auth/approle/role/$AGENT_ROLE/secret-id" 2>/dev/null \
    | jq -r '.wrap_info.token // empty')
  if [ -z "$wrapped" ]; then
    log "could not mint a wrapped secret id for Vault Agent"
    return 1
  fi

  umask 077
  printf '%s' "$wrapped" > "$wrapfile"
  chmod 0644 "$wrapfile"
  log "minted a wrapped secret id for Vault Agent (ttl $WRAP_TTL, single use)"
}

# reconcile brings Vault to unsealed if it can, and says so only when something
# changed. A quiet log is the normal state.
reconcile() {
  st=$(status)
  if [ "$(boolfield "$st" initialized)" != "true" ]; then
    initialise || return 1
    st=$(status)
  fi
  if [ "$(boolfield "$st" sealed)" = "true" ]; then
    log "Vault is sealed, applying up to $THRESHOLD shares"
    unseal || return 1
    if [ "$(boolfield "$(status)" sealed)" = "true" ]; then
      log "still sealed: the key file does not match this storage"
      return 1
    fi
    log "unsealed"
  fi
  ensure_agent_credentials || true
  return 0
}

log "watching $VAULT_ADDR every ${INTERVAL}s"
while :; do
  # Vault may not have opened its listener yet, or may be restarting. A status
  # with no fields in it is a wait, not an error.
  if [ "$(boolfield "$(status)" sealed)" = "" ]; then
    sleep "$INTERVAL"
    continue
  fi
  reconcile || log "will retry in ${INTERVAL}s"
  sleep "$INTERVAL"
done
