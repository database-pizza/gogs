# Deploying git.database.pizza

This directory holds the rollout artifacts for running Gogs (this fork, with the
experimental PizzaSQL backend) on the existing FreeBSD arm64 host that already
serves database.pizza.

## Status

The initial deployment used these artifacts on 2026-09-10. The Gogs service is
bound to `127.0.0.1:3001`, uses the hosted `gogs/gogs` database through the
managed PostgreSQL proxy, and is registered as a FreeBSD `gogs` service. Releases
are staged as timestamped directories under `releases/` and selected with the
`current` symlink, so a rollout or rollback is a symlink switch plus a service
restart. No secrets, API keys, or passwords are committed anywhere in this
repository.

## Shape

Gogs runs under a dedicated, unprivileged `gogs` OS account with no interactive
shell. Each release is a timestamped executable directory and `current` is a
symlink to the active one. A `shared` directory holds everything that survives a
binary upgrade:

| Path | Purpose |
| --- | --- |
| `/home/pizzadatabase/gogs/releases/<timestamp>/gogs` | Release executable, mode `0555`. |
| `/home/pizzadatabase/gogs/current` | Symlink to the active release directory. |
| `/home/pizzadatabase/gogs/shared/custom/conf/app.ini` | Custom configuration. |
| `/home/pizzadatabase/gogs/shared/data` | Application data (sessions, avatars, attachments, logs, LFS). |
| `/home/pizzadatabase/gogs/shared/repos` | Managed repositories. |

The top-level `/home/pizzadatabase/gogs` directory and release binaries are owned
by `root:pizzadatabase`; release directories are mode `0755` and binaries are mode
`0555`. `shared` is owned by `gogs:gogs` with mode `0700`. `GOGS_WORK_DIR` points
at `current` and `GOGS_CUSTOM` at `shared/custom`, so the config and data live
outside the release directory and survive an upgrade.

## Database

The backend is the hosted, managed PizzaSQL database `gogs/gogs`, reached through
a local proxy bound to `127.0.0.1:5432` and authenticated with a scoped API key.
The proxy terminates TLS upstream, so Gogs connects to the loopback proxy with
`SSL_MODE = disable`. The scoped API key is stored only in `app.ini` on the host
as the `[database] PASSWORD` value; it must never be committed, logged, or passed
as a process argument.

## Prerequisites

- The managed `gogs/gogs` database exists and its scoped API key is available.
- A local proxy forwards `127.0.0.1:5432` to the managed service and keeps the
  loopback hop confidential.
- FreeBSD `daemon(8)` (base system) is present; no third-party supervisor is
  needed.

## OS user

Create the dedicated account once, with no login shell and a home under the
`pizzadatabase` tree:

```sh
pw useradd -n gogs -u 901 -d /home/pizzadatabase/gogs/shared -s /usr/sbin/nologin -c "Gogs"
```

Pick an unused UID; the example `901` is a placeholder. Create the release and
shared trees. `current` is created later as a symlink by the release step, and
only `shared` is owned by the service account:

```sh
mkdir -p /home/pizzadatabase/gogs/releases \
  /home/pizzadatabase/gogs/shared/custom/conf \
  /home/pizzadatabase/gogs/shared/data /home/pizzadatabase/gogs/shared/repos
chown root:pizzadatabase /home/pizzadatabase/gogs /home/pizzadatabase/gogs/releases
chown -R gogs:gogs /home/pizzadatabase/gogs/shared
```

## Configuration

Copy `app.ini.example` to `/home/pizzadatabase/gogs/shared/custom/conf/app.ini`
and fill in the placeholders, then lock it down:

```sh
install -m 0640 -o root -g gogs deploy/app.ini.example /home/pizzadatabase/gogs/shared/custom/conf/app.ini
```

Generate the application secret on the host; never reuse the example placeholder:

```sh
openssl rand -hex 32
```

Set the generated value as `[security] SECRET_KEY` and the scoped API key as
`[database] PASSWORD`. The config enables `INSTALL_LOCK = true` and
`DISABLE_REGISTRATION = true`, binds `127.0.0.1:3001`, disables SSH (including
rewriting `authorized_keys`), and keeps the installer behind the lock so no
installer API key is ever exposed.

## Service

Install the rc.d script and enable it:

```sh
install -m 0555 deploy/gogs.rc /usr/local/etc/rc.d/gogs
sysrc gogs_enable=YES
```

`gogs.rc` runs `daemon(8)` as the supervisor with `-r -R 5`. The supervisor
process ID is written to `/var/run/gogs.pid` via the parent pidfile flag `-P`,
and `procname` is `/usr/sbin/daemon`. Because the pidfile refers to the
supervisor, `service gogs stop` signals the supervisor, which terminates the Gogs
child gracefully and exits; the child is only restarted when it dies on its own,
so stopping the service does not cause an immediate restart loop. Output goes to
`/var/log/gogs.log`, which `daemon(8)` opens as root before dropping to the
`gogs` user, so the log stays root-owned and cannot be replaced by the service
account. See `daemon(8)` for the `-r`, `-R`, `-P`, and `-o` semantics.

## Bootstrap

The first `web` start creates the database schema, and the administrator is then
created from the CLI. The two must happen in this order because `NewEngine` runs
migrations on boot while `admin create-user` only opens a connection and assumes
the schema already exists:

1. Stage a release first (see [Release](#release)), then start the service once
   and let it migrate and settle:

   ```sh
   service gogs start
   ```

2. Write the administrator password to a private file and create the account:

   ```sh
   umask 077
   printf '%s\n' '<generated-admin-password>' > /tmp/gogs-admin-password
   sudo -u gogs ./deploy/create-admin.sh \
     --name <admin-username> --email <admin-email> --password-file /tmp/gogs-admin-password --admin
   rm /tmp/gogs-admin-password
   ```

   `create-user` accepts `--password` or `--password-file` but never both; the
   file must be non-empty and not readable by group or others (mode `0600` or
   stricter), and only a trailing newline is trimmed so significant whitespace in
   the password is preserved. The password is never echoed, logged, or passed as
   a process argument.

3. Restart the service so it runs under the supervisor cleanly:

   ```sh
   service gogs restart
   ```

## Reverse proxy

Caddy already fronts the host. Add a site for `git.database.pizza` that proxies
to `127.0.0.1:3001` and forwards the `Host` header. Gogs is configured with
`EXTERNAL_URL = https://git.database.pizza/`, so generated links and cookies
match the public origin.

## Release

Build the binary with the same stamps as the `build` task, then stage it as a new
timestamped release and switch `current` to it. Building on the host is simplest;
cross-compile from another host with `GOOS=freebsd GOARCH=arm64 CGO_ENABLED=0`.

```sh
commit=$(git rev-parse HEAD)
build_time=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
GOOS=freebsd GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-X gogs.io/gogs/internal/conf.BuildTime=$build_time -X gogs.io/gogs/internal/conf.BuildCommit=$commit" \
  -o .bin/gogs .
```

Record the active release before switching so rollback is a symlink flip back to
it, then stage the binary, switch the symlink, and restart:

```sh
readlink /home/pizzadatabase/gogs/current

base=/home/pizzadatabase/gogs
ts=$(date +%Y%m%d-%H%M%S)
install -d -o root -g pizzadatabase "$base/releases/$ts"
install -m 0555 -o root -g pizzadatabase .bin/gogs "$base/releases/$ts/gogs"
ln -s "releases/$ts" "$base/current.next"
mv -hf "$base/current.next" "$base/current"
service gogs restart
```

Verify the listener and the public site, including a DB-backed page:

```sh
sockstat -46l | grep 3001
curl -fsS -o /dev/null https://git.database.pizza/
curl -fsS -o /dev/null https://git.database.pizza/explore/repos
```

Previous release directories stay in place, so a rollback never needs a rebuild.

## Backup and rollback

The PizzaSQL backend does not support Gogs' built-in `backup`/`restore` commands
(see `DATABASE_PIZZA.md`), so the approved backup plan is external. Before any
rollout step, snapshot the managed `gogs/gogs` database through the managed
service and archive the `shared` directory and the active release directory under
`releases/`.

Rollback is therefore:

1. `service gogs stop`.
2. Restore the managed database snapshot and the previous `shared` tree.
3. Point `current` at the previous release directory:

   ```sh
   ln -s releases/<previous> /home/pizzadatabase/gogs/current.next
   mv -hf /home/pizzadatabase/gogs/current.next /home/pizzadatabase/gogs/current
   ```

4. `service gogs start`.

Because configuration and data live in `shared` and the release in `current`,
swapping either is independent of the other.

## Verification

With the service running, confirm the listener, the public site, and the health
endpoints:

```sh
service gogs status
curl -fsS -o /dev/null https://git.database.pizza/
curl -fsS -o /dev/null https://git.database.pizza/user/login
```

Review the root-owned log without leaking configuration:

```sh
tail -n 50 /var/log/gogs.log
```
