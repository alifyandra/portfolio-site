# Oracle Cloud dev box

A codified 24/7 remote development box on Oracle Cloud (Ampere A1, aarch64,
Ubuntu 24.04). Separate from the production AWS host in `deploy/terraform/`:
this is where you edit and test the codebase, not where it runs for users.

Setup is split into two layers, the same split the AWS host uses.

## Layer 1: the machine (`setup.sh`)

System provisioning, run as root. Idempotent, safe to re-run.

- Creates the `alifyandra` dev user (sudo + docker groups, passwordless sudo),
  and copies the seed user's `authorized_keys` so the same SSH key works.
- Adds a 4 GB swapfile (the single core has no swap by default).
- Installs Docker Engine + Compose plugin, Node.js (LTS), and Claude Code.
- Sets the timezone.

```bash
sudo bash setup.sh                       # defaults
DEV_USER=alifyandra NODE_MAJOR=22 sudo -E bash setup.sh   # overrides
```

This file is written to double as Terraform cloud-init: cloud-init runs a
`user_data` script that begins with `#!` as root on first boot, so dropping
`setup.sh` into `user_data` provisions an identical box with no manual step.
See "Terraform later" below.

## Layer 2: the project (`bootstrap-project.sh`)

Brings the portfolio stack up. Run as the dev user (needs the docker group,
which a fresh login picks up after `setup.sh`).

- Clones (or pulls) the repo into `~/Projects/portfolio-site`.
- `make setup` (`.env` + frontend deps + orval codegen from the committed
  `openapi.yaml`, no daemon needed).
- `docker compose up --build -d` (detached, unlike `make up` which is
  foreground), waits for `/healthz`, then seeds starter projects.

```bash
sudo -iu alifyandra bash bootstrap-project.sh
```

## Connecting and working 24/7

Full reference: [`CHEATSHEET.md`](CHEATSHEET.md). The short version:

```bash
ssh oracle          # normal login; prints a one-line tmux hint
work                # enter (create-or-attach) the persistent "dev" session
cc                  # Claude Code in the repo (alias defined in devrc.sh)
```

`bootstrap-project.sh` sources [`devrc.sh`](devrc.sh) from the dev user's
`~/.bashrc`. That gives two things:

- **A login hint (not auto-attach):** an interactive SSH login prints one line
  reminding you to run `work`. We deliberately do not `exec` into tmux on login
  (surprising, and it hijacks the shell). You opt in with `work`, which
  create-or-attaches the `dev` session. Detach with `Ctrl-b d` or by closing the
  client; the session (and any running Claude / dev server) keeps going.
  Reconnect, run `work` (or `att`), and you are back. This is what makes work
  survive a dropped connection: a bare `claude` on a raw shell dies on SIGHUP;
  tmux owns the process instead.
- **Short commands:** `cc` (claude in the current repo), `pf`/`fb`/`wa` (cd to
  portfolio-site / finance-broker / whatsapp-sidecar), `work`/`att`/`s` (tmux),
  `up`/`down`/`logs`/`dps` (portfolio stack), `fe` (frontend), `ccbg` (headless).

This box holds three repos under `~/Projects`: `portfolio-site` (public
monorepo), and the two private siblings `finance-broker` (CommBank home broker,
on `feat/broker-app`) and `whatsapp-sidecar` (whatsapp-web.js engine). Each
private repo has its own `CLAUDE.md`. They are cloned over SSH
(`git@github.com:alifyandra/...`, the box key is registered) and must never be
pushed into the public repo.

Details:

- API: `http://localhost:8080` (`/healthz`, `/docs`, `/api/projects`)
- Frontend: `fe` (or `cd frontend && npm run dev`) on `:3000`
- Claude Code needs a one-time interactive login on the box: run `claude` and
  follow the prompt. Auth is per-user and cannot be scripted (already done on
  the live box).

Reach the dev servers from your laptop with an SSH tunnel:

```bash
ssh -L 3000:localhost:3000 -L 8080:localhost:8080 oracle
```

## The box

- Oracle Ampere A1, aarch64, Ubuntu 24.04, 1 OCPU / 6 GB / ~45 GB disk.
- 1 OCPU is tight for the full Docker stack plus a Next.js dev build. The A1
  free allowance is up to 4 OCPU / 24 GB total, so bump the shape if it drags.

## Terraform later (not yet done)

To make the box itself reproducible (destroy and recreate on demand), add the
`oci` provider Terraform in a sibling directory:

- `oci_core_instance` (Ampere A1 shape) + VCN + subnet + security list + the
  SSH key, with `metadata.user_data = base64encode(file("setup.sh"))` so the
  machine self-provisions on first boot.
- State backend: OCI Object Storage, or local state for a personal box.
- Requires OCI API credentials (tenancy + user OCID, a generated API signing
  key). That is the one manual prerequisite before `terraform apply` works.

`bootstrap-project.sh` stays a post-boot step (or a cloud-init `runcmd`), kept
out of `user_data` so a slow first-boot `docker build` never blocks the boot.
