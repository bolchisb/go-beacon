# The relay's Vault, with its configuration and operational scripts baked in, so
# a deployment is one image pull rather than an image plus a set of files that
# have to be kept in step with it.
#
# What is deliberately NOT in here: the unseal keys, the root token, the AppRole
# secret id. This image is published to a registry. Anything compiled into it is
# published with it -- which is the whole reason agent credentials are not baked
# into the agent either. Secrets arrive at runtime, on a mount.
FROM docker.io/hashicorp/vault:2.0.4

# The base image runs as uid 100 (vault), which cannot write to the package
# database. Root is borrowed for the install and given straight back, so the
# published image still runs unprivileged.
USER root
# jq parses `vault status -format=json` in the unseal script. The base image
# does not carry it.
RUN apk add --no-cache jq

# Deliberately NOT /vault/config: that directory is auto-loaded by the base
# image's entrypoint, and Vault merges every .hcl it finds there. Two configs in
# one directory means two listener blocks on the same address, and Vault fails
# to bind. Each config is selected explicitly by path instead.
COPY vault/config/ /vault/beacon/
COPY vault/agent.hcl /vault/beacon/agent.hcl
COPY vault/unseal.sh /usr/local/bin/beacon-vault-unseal
COPY vault/bootstrap.sh /usr/local/bin/beacon-vault-bootstrap
RUN chmod 0755 /usr/local/bin/beacon-vault-unseal /usr/local/bin/beacon-vault-bootstrap

# The mount points for the two runtime volumes, created here so they carry the
# right owner. Docker gives a fresh named volume the ownership of the directory
# it covers in the image, and root:root if that directory does not exist -- at
# which point nothing running as uid 100 can write the unseal keys or the token
# sink, and the stack comes up with Vault stuck uninitialised. Rootless podman
# happens to remap the owner and hides this, so the directories must exist for
# a docker deployment to work at all.
RUN mkdir -p /unseal /agent \
 && chown vault:vault /unseal /agent \
 && chmod 0700 /unseal \
 && chmod 0755 /agent

USER vault

# Same entrypoint and default command as the base image: this stays a Vault, and
# the unseal helper runs as its own container rather than wrapping the server.
ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["server", "-config=/vault/beacon/vault.hcl"]
