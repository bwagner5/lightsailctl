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