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

```bash
ssh -i ~/.ssh/id_ed25519 alifyandra@<box-ip>
tmux new -s dev          # run claude / dev servers inside tmux
tmux attach -t dev       # survives disconnects; reattach after reconnecting
```

- API: `http://localhost:8080` (`/healthz`, `/docs`, `/api/projects`)
- Frontend: `cd ~/Projects/portfolio-site/frontend && npm run dev` (:3000)
- Claude Code needs a one-time interactive login on the box: run `claude` and
  follow the prompt. Auth is per-user and cannot be scripted.

Reach the dev servers from your laptop with an SSH tunnel:

```bash
ssh -i ~/.ssh/id_ed25519 -L 3000:localhost:3000 -L 8080:localhost:8080 alifyandra@<box-ip>
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
