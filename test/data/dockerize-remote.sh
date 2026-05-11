#!/usr/bin/env bash
set -euo pipefail

echo "🐳 Installing docker and the compose plugin 🐋"

dnf install -y docker
systemctl enable --now docker

DOCKER_CONFIG=${DOCKER_CONFIG:-/usr/local/lib/docker}
mkdir -p ${DOCKER_CONFIG}/cli-plugins
curl -SL https://github.com/docker/compose/releases/download/v5.0.1/docker-compose-linux-x86_64 -o $DOCKER_CONFIG/cli-plugins/docker-compose
curl -SL https://github.com/docker/buildx/releases/download/v0.21.2/buildx-v0.21.2.linux-amd64 -o $DOCKER_CONFIG/cli-plugins/docker-buildx

chmod +x ${DOCKER_CONFIG}/cli-plugins/docker-compose ${DOCKER_CONFIG}/cli-plugins/docker-buildx
usermod -aG docker ec2-user

docker compose version

echo "✅ Successfully installed docker and the compose plugin 🐋"

# ── Service user for the watcher ─────────────────────────────────────
# The watcher runs as `lightsailctl` (not root) so build tooling like
# `pack` has a real $HOME and so blast radius is bounded. The user is
# in the docker group so the watcher can drive `docker build` / `pack
# build` / `docker compose` against the local daemon, and owns
# /opt/lightsail so it can write deploy state without sudo.
echo "👤 Creating lightsailctl service user"
id -u lightsailctl >/dev/null 2>&1 || useradd --system --create-home --shell /bin/bash --groups docker lightsailctl
# Make sure a pre-existing user picks up docker group access on
# re-bootstrap (e.g. when the docker install came after the user).
usermod -aG docker lightsailctl
mkdir -p /opt/lightsail
chown -R lightsailctl:lightsailctl /opt/lightsail
echo "✅ lightsailctl user ready"

# ── Cloud Native Buildpacks: pre-install `pack` and warm paketo images ─
# Newly-bootstrapped instances support zero-config deploys (no
# Dockerfile, no compose) via Cloud Native Buildpacks. We install the
# `pack` CLI and pre-pull the paketo builder + run images so the first
# buildpack deploy doesn't pay ~500 MB of pull latency on a path the
# user is actively watching.
#
# The version is pinned; bumping is a one-line change gated by the
# integration tests.
PACK_VERSION="0.36.0"
PACK_URL="https://github.com/buildpacks/pack/releases/download/v${PACK_VERSION}/pack-v${PACK_VERSION}-linux.tgz"
echo "📦 Installing Cloud Native Buildpacks pack v${PACK_VERSION}"
curl -SL "${PACK_URL}" | tar -xz -C /usr/local/bin pack
chmod +x /usr/local/bin/pack
# `pack version` needs $HOME to resolve its config dir (~/.pack);
# cloud-init runs user-data with no HOME set, so we provide one
# explicitly. Skipping the smoke test would also work, but a clear
# failure here is more useful than a mystery later.
HOME=/root pack version

# Pre-warm the paketo builder + run images so the first deploy is
# fast. `docker pull` is idempotent, so re-running the script is
# safe; pre-existing instances that miss this warming pay the pull
# cost on their first buildpack deploy via the agent's
# `pulling-builder` phase.
echo "🔥 Warming paketo builder + run images"
docker pull paketobuildpacks/builder-jammy-base
docker pull paketobuildpacks/run-jammy-base

echo "✅ pack ready and paketo images warm 🚀"