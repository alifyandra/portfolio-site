#!/usr/bin/env bash
#
# Bring the portfolio stack up on the dev box. Run as the dev user:
#   sudo -iu alifyandra bash bootstrap-project.sh
#
# Idempotent. Requires setup.sh to have run first (git, node, docker, make).
# Uses detached compose (make up runs in the foreground) so the script returns.
#
# Tunables (env overrides):
#   REPO_URL=https://github.com/alifyandra/portfolio-site.git
#   PROJECTS_DIR=$HOME/Projects
set -euo pipefail

REPO_URL="${REPO_URL:-https://github.com/alifyandra/portfolio-site.git}"
PROJECTS_DIR="${PROJECTS_DIR:-$HOME/Projects}"
REPO_DIR="${PROJECTS_DIR}/portfolio-site"

log() { printf '\n\033[1;34m==>\033[0m %s\n' "$*"; }

id -nG | tr ' ' '\n' | grep -qx docker || {
  echo "current user is not in the docker group yet (log out/in after setup.sh)"; exit 1; }

log "Clone / update ${REPO_URL}"
mkdir -p "${PROJECTS_DIR}"
if [ -d "${REPO_DIR}/.git" ]; then
  git -C "${REPO_DIR}" pull --ff-only || true
else
  git clone "${REPO_URL}" "${REPO_DIR}"
fi
cd "${REPO_DIR}"

log "make setup (.env + frontend deps + codegen from committed openapi.yaml)"
make setup

log "Build + start stack detached (Postgres, Redis, MinIO, API :8080)"
docker compose up --build -d

log "Wait for API health"
ok=no
for _ in $(seq 1 40); do
  if curl -fsS http://localhost:8080/healthz >/dev/null 2>&1; then ok=yes; break; fi
  sleep 3
done
[ "${ok}" = yes ] && echo "API healthy" || echo "API not healthy yet (check: docker compose logs api)"

log "Seed starter projects"
docker compose run --rm api seed || true

log "Shell helpers (source devrc.sh from ~/.bashrc)"
# Idempotent: adds the source line once. devrc.sh defines the short commands
# (cc, work, att, up/down/logs, ...) and prints a login hint nudging you to run
# `work` for a persistent tmux session so work survives disconnects. It does
# not auto-attach.
HOOK_MARK="# portfolio dev-box helpers (deploy/oracle-dev/devrc.sh)"
if ! grep -qF "${HOOK_MARK}" "${HOME}/.bashrc" 2>/dev/null; then
  cat >> "${HOME}/.bashrc" <<EOF

${HOOK_MARK}
[ -f "${REPO_DIR}/deploy/oracle-dev/devrc.sh" ] && . "${REPO_DIR}/deploy/oracle-dev/devrc.sh"
EOF
fi

cat <<EOF

Stack is up in ${REPO_DIR}
  API:      http://localhost:8080   (/healthz  /docs  /api/projects)
  Frontend: cd frontend && npm run dev   (Next.js on :3000)
  Logs:     docker compose logs -f api
  Stop:     docker compose down

Shell helpers are live on next login (or: . ~/.bashrc). See the cheatsheet:
  deploy/oracle-dev/CHEATSHEET.md
Login prints a hint: run 'work' to enter a persistent tmux "dev" session, so
Claude Code and dev servers survive a dropped connection. Detach with Ctrl-b d,
rejoin with 'att'. Short commands: cc (claude), work/att (tmux), up/down/logs.
EOF
