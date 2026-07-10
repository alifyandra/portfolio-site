#!/usr/bin/env bash
#
# Oracle Cloud dev box provisioning (system layer).
#
# Runs two ways, same result:
#   1. Manually on a running box:  sudo bash setup.sh
#   2. As Terraform cloud-init:    drop this file into the instance user_data
#      (cloud-init runs a script beginning with #! as root on first boot)
#
# Idempotent: safe to re-run. It provisions the machine only (system packages,
# a dev user, swap, Docker, Node, Claude Code). Bringing the portfolio stack up
# lives in bootstrap-project.sh so the daemon-dependent bits stay out of boot.
#
# Tunables (env overrides):
#   DEV_USER=alifyandra  NODE_MAJOR=22  SWAP_GB=4
#   TIMEZONE=Australia/Melbourne  SEED_USER=ubuntu
set -euo pipefail

DEV_USER="${DEV_USER:-alifyandra}"
NODE_MAJOR="${NODE_MAJOR:-22}"
SWAP_GB="${SWAP_GB:-4}"
TIMEZONE="${TIMEZONE:-Australia/Melbourne}"
SEED_USER="${SEED_USER:-ubuntu}"   # user whose authorized_keys we copy for SSH

log() { printf '\n\033[1;34m==>\033[0m %s\n' "$*"; }

[ "$(id -u)" -eq 0 ] || { echo "run as root (use sudo)"; exit 1; }

export DEBIAN_FRONTEND=noninteractive
APT_OPTS="-y -o DPkg::Lock::Timeout=300"

log "Base packages"
apt-get $APT_OPTS update
# shellcheck disable=SC2086
apt-get $APT_OPTS install --no-install-recommends \
  ca-certificates curl gnupg lsb-release \
  git make build-essential unzip \
  tmux htop ripgrep jq

log "Timezone -> ${TIMEZONE}"
timedatectl set-timezone "${TIMEZONE}" || true

log "Swap (${SWAP_GB}G)"
if ! swapon --show | grep -q '/swapfile'; then
  if ! fallocate -l "${SWAP_GB}G" /swapfile 2>/dev/null; then
    dd if=/dev/zero of=/swapfile bs=1M count=$((SWAP_GB * 1024))
  fi
  chmod 600 /swapfile
  mkswap /swapfile
  swapon /swapfile
  grep -q '^/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

log "Dev user: ${DEV_USER}"
id "${DEV_USER}" >/dev/null 2>&1 || useradd -m -s /bin/bash "${DEV_USER}"
usermod -aG sudo "${DEV_USER}"
printf '%s ALL=(ALL) NOPASSWD:ALL\n' "${DEV_USER}" > "/etc/sudoers.d/90-${DEV_USER}"
chmod 440 "/etc/sudoers.d/90-${DEV_USER}"

log "SSH access for ${DEV_USER} (copied from ${SEED_USER})"
if [ -f "/home/${SEED_USER}/.ssh/authorized_keys" ]; then
  install -d -m 700 -o "${DEV_USER}" -g "${DEV_USER}" "/home/${DEV_USER}/.ssh"
  install -m 600 -o "${DEV_USER}" -g "${DEV_USER}" \
    "/home/${SEED_USER}/.ssh/authorized_keys" \
    "/home/${DEV_USER}/.ssh/authorized_keys"
fi

log "Docker Engine + Compose plugin"
if ! command -v docker >/dev/null 2>&1; then
  install -d -m 0755 /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
    | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  chmod a+r /etc/apt/keyrings/docker.gpg
  printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu %s stable\n' \
    "$(dpkg --print-architecture)" "$(. /etc/os-release && echo "$VERSION_CODENAME")" \
    > /etc/apt/sources.list.d/docker.list
  apt-get $APT_OPTS update
  apt-get $APT_OPTS install docker-ce docker-ce-cli containerd.io \
    docker-buildx-plugin docker-compose-plugin
fi
systemctl enable --now docker
usermod -aG docker "${DEV_USER}"

log "Node.js ${NODE_MAJOR}.x + npm"
have_node_major="$(command -v node >/dev/null 2>&1 && node -v | sed 's/^v//;s/\..*//' || echo none)"
if [ "${have_node_major}" != "${NODE_MAJOR}" ]; then
  curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash -
  apt-get $APT_OPTS install nodejs
fi

log "Claude Code (global npm)"
npm install -g @anthropic-ai/claude-code

log "System provisioned."
cat <<EOF

Done. Next:
  Bring the portfolio stack up as the dev user:
    sudo -iu ${DEV_USER} bash bootstrap-project.sh
  Authenticate Claude Code (interactive, once, as the dev user):
    sudo -iu ${DEV_USER} claude
EOF
