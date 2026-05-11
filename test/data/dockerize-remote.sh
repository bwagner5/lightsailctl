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
pack version

# Pre-warm the paketo builder + run images so the first deploy is
# fast. `docker pull` is idempotent, so re-running the script is
# safe; pre-existing instances that miss this warming pay the pull
# cost on their first buildpack deploy via the agent's
# `pulling-builder` phase.
echo "🔥 Warming paketo builder + run images"
docker pull paketobuildpacks/builder-jammy-base
docker pull paketobuildpacks/run-jammy-base

echo "✅ pack ready and paketo images warm 🚀"