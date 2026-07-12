# Oracle dev box cheatsheet

Connecting from a laptop terminal or Termius, and keeping Claude Code alive
across disconnects. The box is `ssh oracle` (alias in `~/.ssh/config`).

## The one rule

Never run `claude` on a bare SSH shell. A dropped connection (laptop sleep,
phone call, WiFi blip) sends SIGHUP and kills it, losing the session. Run it
inside tmux instead: tmux owns the process, SSH is just a window onto it.

`ssh oracle` logs in normally and prints a one-line hint. Run `work` to enter
the persistent tmux session named `dev` (create-or-attach). Everything you run
inside it survives a disconnect.

## Daily loop

```bash
ssh oracle          # normal login; prints the tmux hint
work                # enter (create-or-attach) the persistent "dev" session
cc                  # Claude Code in the repo (alias: cd + claude)
```

Disconnect any time: press `Ctrl-b` then `d` (detach), or just close the
terminal / Termius. Claude keeps running on the box.

Reconnect from anywhere (other laptop, phone): `ssh oracle` puts you back in
the same live `dev` session.

## Short commands (from `devrc.sh`)

| Command | Does |
|---|---|
| `cc [args]` | Claude Code in the current repo (cd first). `cc --continue` resumes last, `cc --resume` picks one |
| `pf` / `fb` / `wa` | cd to portfolio-site / finance-broker / whatsapp-sidecar |
| `dev` | cd to the workspace root (`~/Projects`) |
| `work [name]` | Create-or-attach a tmux session (default `dev`) |
| `att [name]` | Attach to a session (default `dev`) |
| `s` | List tmux sessions |
| `ccbg <name> "<task>"` | Headless Claude task in a detached session; rejoin with `att <name>` |
| `up` / `down` | Start / stop the portfolio-site Docker stack (detached) |
| `logs` | Follow portfolio-site API + service logs |
| `dps` | portfolio-site Docker compose status |
| `fe` | portfolio-site frontend dev server (`:3000`) |

The repos on this box:

| Repo | Path | What | Public? |
|---|---|---|---|
| `portfolio-site` | `~/Projects/portfolio-site` | Go + Next.js monorepo (the platform) | public |
| `finance-broker` | `~/Projects/finance-broker` | CommBank home broker (`feat/broker-app`) | private |
| `whatsapp-sidecar` | `~/Projects/whatsapp-sidecar` | whatsapp-web.js sidecar | private |

`up`/`down`/`logs`/`fe` drive the portfolio-site stack. The other two are Node
apps with their own run commands (`npm test`, `npm start`, `docker compose up`);
see each repo's `CLAUDE.md`.

## Running several things at once

Named sessions, one per task:

```bash
work backend        # a Claude session on the backend
# Ctrl-b d to detach
work frontend       # another on the frontend
s                   # list them
att backend         # jump back into one
```

Or split one session into panes: `Ctrl-b c` new window, `Ctrl-b "` split
horizontal, `Ctrl-b %` split vertical, `Ctrl-b <arrow>` move between panes.
Handy for `cc` in one pane and `logs` in another.

## tmux keys worth knowing

Prefix is `Ctrl-b`, then:

| Key | Action |
|---|---|
| `d` | Detach (leave everything running) |
| `c` | New window |
| `n` / `p` | Next / previous window |
| `"` / `%` | Split pane horizontal / vertical |
| `arrow` | Move between panes |
| `[` | Scroll mode (`q` to exit); needed to scroll long Claude output |

## Reaching the dev servers from your laptop

The API and frontend listen on the box's localhost. Tunnel them over SSH:

```bash
ssh -L 3000:localhost:3000 -L 8080:localhost:8080 oracle
```

Then on the laptop: `localhost:8080/docs`, `localhost:8080/api/projects`, and
`localhost:3000` if the frontend dev server is running.

## Headless / fire-and-forget runs

`ccbg` starts a Claude task with no attached TUI and returns immediately:

```bash
ccbg tests "run make test and fix any failures"
att tests           # check on it later
```

Headless mode cannot answer permission prompts. For a task that must run fully
unattended, launch it by hand with explicit flags:

```bash
tmux new -d -s job 'cd ~/Projects/portfolio-site && \
  claude -p "..." --dangerously-skip-permissions; exec bash'
```

Only skip permissions on a scoped, trusted task, since nobody is there to
approve actions.

## Termius (phone)

- Host: the box IP (kept in your `~/.ssh/config` as the `oracle` alias, not in
  this repo), user `alifyandra`, key `id_ed25519`. Connect, then run `work` to
  enter the `dev` session (or save a snippet that runs it on connect).
- Mobile networks drop often; that is exactly why tmux matters. A dropped
  connection never loses work, you just reopen the host, run `att`, and you are
  back.
- Use the port-forwarding tab if you want the `:3000` / `:8080` tunnel on the
  phone too.

## If auto-tmux ever gets in the way

To connect without landing in tmux (for a quick one-off), run a command
directly: `ssh oracle "docker compose -f ~/Projects/portfolio-site/docker-compose.yml ps"`.
Non-interactive commands skip the auto-tmux entirely.
