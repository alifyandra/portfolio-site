# deploy/oracle-dev/devrc.sh
#
# Shell helpers for the Oracle dev box. Sourced from the dev user's ~/.bashrc
# (bootstrap-project.sh wires the source line). Safe to source anywhere:
# it only acts on interactive login shells.
#
# Two jobs:
#   1. Short command aliases for the daily loop (claude, tmux, the stack).
#   2. A one-line login hint nudging you to run `work` to enter a persistent
#      tmux session (so work survives a dropped laptop/phone connection). It
#      does NOT auto-attach: you connect normally, then opt in with `work`.

# Do nothing for non-interactive shells (scp, `ssh box "cmd"`, cron).
case $- in
  *i*) ;;
  *) return 0 2>/dev/null ;;
esac

PORTFOLIO_DIR="${PORTFOLIO_DIR:-$HOME/Projects/portfolio-site}"
PROJECTS_DIR="${PROJECTS_DIR:-$HOME/Projects}"

# --- navigation (this box holds several repos) ---
alias dev='cd "$PROJECTS_DIR"'              # workspace root
alias pf='cd "$PORTFOLIO_DIR"'              # portfolio-site (public monorepo)
alias fb='cd "$PROJECTS_DIR/finance-broker"'    # finance-broker (private)
alias wa='cd "$PROJECTS_DIR/whatsapp-sidecar"'  # whatsapp-sidecar (private)

# --- claude ---
# cc: interactive Claude in the CURRENT repo (cd there first: `pf && cc`,
# `fb && cc`, ...). `cc --continue` resumes the last session, `cc --resume`
# picks one from the list.
cc() { claude "$@"; }

# ccbg <name> "<task>": run a headless Claude task in a detached tmux session.
# Survives disconnect. Rejoin with `att <name>` to watch or take over.
# For a fully autonomous run add flags to the task string is not possible here;
# use a raw `tmux new -d -s <name> 'claude -p "..." --permission-mode ...'`.
ccbg() {
  local name="${1:?usage: ccbg <name> \"<task>\"}"; shift
  local task="${*:?usage: ccbg <name> \"<task>\"}"
  tmux new -d -s "$name" "cd '$PORTFOLIO_DIR' && claude -p \"$task\"; exec bash"
  echo "started '$name'  ->  att $name"
}

# --- tmux ---
alias s='tmux ls 2>/dev/null || echo "no sessions"'          # list sessions
att() { tmux attach -t "${1:-dev}"; }                          # att [name] (default dev)
work() { tmux new -A -s "${1:-dev}"; }                         # create-or-attach

# --- stack ---
alias up='cd "$PORTFOLIO_DIR" && docker compose up -d'
alias down='cd "$PORTFOLIO_DIR" && docker compose down'
alias logs='cd "$PORTFOLIO_DIR" && docker compose logs -f'
alias dps='cd "$PORTFOLIO_DIR" && docker compose ps'
alias fe='cd "$PORTFOLIO_DIR/frontend" && npm run dev'

# --- welcome hint: nudge toward a persistent tmux session (no auto-attach) ---
# We do NOT exec into tmux on login (that is surprising). Instead, an
# interactive SSH login that is not already inside tmux prints one line telling
# you the command. Skipped for `ssh box "cmd"` / scp (no ssh tty) and inside tmux.
if [ -n "${SSH_TTY:-}" ] && [ -z "${TMUX:-}" ] && command -v tmux >/dev/null 2>&1; then
  if tmux has-session -t dev 2>/dev/null; then
    printf '\n  tmux: session \033[1mdev\033[0m is running \342\200\224 attach with \033[1matt\033[0m to rejoin your work\n\n'
  else
    printf '\n  tmux: run \033[1mwork\033[0m to start a persistent session (survives disconnects), \033[1matt\033[0m to rejoin\n\n'
  fi
fi
