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

cat <<EOF

Stack is up in ${REPO_DIR}
  API:      http://localhost:8080   (/healthz  /docs  /api/projects)
  Frontend: cd frontend && npm run dev   (Next.js on :3000)
  Logs:     docker compose logs -f api
  Stop:     docker compose down

For 24/7 remote work, run inside tmux so sessions survive disconnects:
  tmux new -s dev      # then start claude / servers inside it
  tmux attach -t dev   # reattach after reconnecting
EOF
