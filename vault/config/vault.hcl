# Production Vault for the beacon relay.
#
# Vault is deliberately not published on any host port: the relay reaches it
# over the compose network and nothing else does. Agents never talk to Vault --
# they are on customer networks, and exposing a Vault to them would be a worse
# exposure than the relay itself.

storage "file" {
  path = "/vault/file"
}

listener "tcp" {
  address = "0.0.0.0:8200"

  # No TLS on this listener. It is not published, and the only client is the
  # relay container on the same private network. This is the same trust
  # boundary already accepted for the proxy-to-relay hop; if that assumption
  # ever stops holding, this is one of the two places that must change.
  tls_disable = true
}

# How Vault refers to itself when it redirects a client. Must be the name the
# relay dials, not localhost.
api_addr = "http://vault:8200"

# The UI is off: there is no reason to have another authenticated surface, and
# every administrative action here is scripted.
ui = false

# mlock is left on, which means the container runtime must actually grant
# CAP_IPC_LOCK -- a rootless runtime does not, and Vault will refuse to start
# rather than run with its keys swappable. That refusal is correct: the fix is
# to grant the capability, not to disable the lock. vault-dev.hcl is the
# development escape hatch and is not for this deployment.
