# Development-only Vault configuration.
#
# Identical to vault.hcl except that memory locking is off. A rootless
# container runtime cannot give Vault an effective CAP_IPC_LOCK, so mlock fails
# and Vault refuses to start at all -- which is what this file works around.
#
# The cost is real and is why this is not the production config: without mlock,
# unsealed key material can be paged out to swap. On the production host the
# answer is to run the container runtime with the capability actually granted,
# not to point production at this file.

storage "file" {
  path = "/vault/file"
}

listener "tcp" {
  address     = "0.0.0.0:8200"
  tls_disable = true
}

api_addr = "http://vault:8200"
ui       = false

disable_mlock = true
