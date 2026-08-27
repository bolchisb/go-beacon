# Vault Agent for the relay.
#
# What this exists to remove: a long-lived AppRole secret_id sitting in .env,
# visible to `docker inspect`, to the process list, and to anything that dumps
# the environment. The relay now never holds a long-lived credential at all --
# it reads a short-lived token from a file that this agent keeps fresh.
#
# The secret_id arrives response-wrapped and single-use. If anyone unwraps it in
# transit, this agent fails to start rather than succeeding quietly, so an
# interception is something you find out about instead of something you don't.
#
# What this does NOT change: the root token still lives in the unseal volume on
# this host, so host compromise still yields everything. This is depth, not a
# new boundary.

pid_file = "/agent/pid"

vault {
  address = "http://vault:8200"
}

auto_auth {
  method "approle" {
    mount_path = "auth/approle"
    config = {
      role_id_file_path   = "/agent/role-id"
      secret_id_file_path = "/agent/wrapped-secret-id"

      # The file above holds a wrapping token, not the secret_id itself.
      # Agent unwraps it against this path and checks the result came back
      # untampered.
      secret_id_response_wrapping_path = "auth/approle/role/beacon-relay/secret-id"

      # Default, stated explicitly because it matters: the wrapped token is
      # consumed and the file removed, so it cannot be replayed. The unseal
      # supervisor mints a replacement when it sees the file gone.
      remove_secret_id_file_after_reading = true
    }
  }

  sink "file" {
    config = {
      path = "/agent/token"

      # The relay container runs as a different uid than this one, and there is
      # no shared user database between them to grant against. The volume is
      # private to these three containers, so world-readable inside it is the
      # pragmatic answer rather than a lax one.
      mode = 0644
    }
  }
}

# No listener: the relay talks to Vault directly and only needs the token.
# Leaving the proxy off keeps this process to one job.
