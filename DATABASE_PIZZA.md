# PizzaSQL backend

Gogs has an experimental `pizzasql` database backend that targets the
[PizzaSQL](https://database.pizza) engine. It is not supported in production.

The backend bridges the existing SQLite SQL generation to the PostgreSQL wire
protocol:

- Both GORM and XORM generate SQL with the SQLite dialect.
- Transport uses the PostgreSQL wire protocol through a `pgx`-backed
  database/sql driver in `internal/pizzadriver`.
- No local SQLite file is created or read. The `PATH` option is ignored.

## Quick start

Run the whole stack locally with the bundled launcher:

```sh
./scripts/pizza-dev.sh --rebuild
```

Run this from the root of the Gogs checkout. It builds Gogs and PizzaSQL, starts
PizzaKV (from `../pizzakv/bin/pizzakv`), PizzaSQL (PostgreSQL wire), and Gogs
web, all bound to `127.0.0.1`:

| Component | Address |
| --- | --- |
| Gogs web | `http://localhost:3000` |
| PizzaSQL PostgreSQL wire | `127.0.0.1:15432` |
| PizzaKV | `127.0.0.1:16379` |

On the first run the web installer appears (`INSTALL_LOCK = false`): create the
admin account through the UI and leave the pre-selected `PizzaSQL` database type
with `SSL_MODE = disable`. Everything is kept under the gitignored `.pizza-dev/`
directory (binaries, `gogs.pkvdb`, repositories, logs, and `custom/conf/app.ini`)
and survives restarts. Press `Ctrl-C` to stop the stack; it shuts down Gogs,
then PizzaSQL, then PizzaKV.

Ports and the data directory can be overridden with `PIZZA_DEV_HTTP_PORT`,
`PIZZA_DEV_PG_PORT`, `PIZZA_DEV_KV_PORT`, and `PIZZA_DEV_DIR` (an absolute
path). Always pass `--rebuild` after pulling changes so a stale cached binary
never hides a fix.

The launcher is for local development only. It is not a deployment, container,
or backup mechanism, and it must not be pointed at production.

## Engine requirements

This backend only works against a locally modified PizzaSQL source tree. It is
not known to work with a deployed engine. The engine must accept SQLite SQL
over the PostgreSQL protocol, specifically:

- A session-local `last_insert_rowid()` function so inserts can report the
  generated row ID.
- SQLite catalog introspection (`sqlite_master`) used by both ORM migrators to
  detect and alter tables and indexes.
- SQLite identifier quoting (backtick) and `AUTOINCREMENT` primary key
  behavior.
- SQLite unique index and constraint semantics that match the tables Gogs
  creates.

Without these the engine will reject migrations and queries at startup.

## LFS table shape

The engine rejects composite primary keys, so the standard `lfs_object` table
(`PRIMARY KEY (repo_id, oid)`) cannot be created as-is. For this backend only,
`lfs_object` is created with a surrogate shape instead:

- A single auto-increment `id` integer primary key.
- A `UNIQUE INDEX idx_lfs_object_repo_oid (repo_id, oid)`.

The application still queries through the original ID-less `LFSObject` model.
This custom shape is applied only on initial table creation, so an existing
broken table is not migrated; use a fresh database.

## Configuration

Loopback-only local configuration, exactly what `scripts/pizza-dev.sh` writes:

```ini
[database]
TYPE = pizzasql
HOST = 127.0.0.1:15432
NAME = gogs
USER = gogs
PASSWORD =
SSL_MODE = disable
```

`SSL_MODE = disable` is required against the local engine because it has no TLS
listener; `require` or `verify-full` would refuse to connect.

### SSL mode default

The shipped `conf/app.ini` sets `SSL_MODE = disable`, so leaving the option
unset resolves to `disable`, not `require`. The driver's `require` fallback is
only reached when the option is explicitly emptied, which is not the default.

### Managed (hosted) database concept

The deployed, managed database.pizza service scopes PizzaSQL databases as
`org/db` and authenticates with an API key. That shape is conceptual and
unverified against this codebase; it does not apply to the local engine. A
hosted engine behind TLS would use `SSL_MODE = require` or `verify-full`. The
local engine has no TLS at all, so reaching it over a network requires a
confidential tunnel (for example SSH); never expose it over plain internet.

Credentials are escaped into a PostgreSQL keyword/value connection string, so
passwords containing quotes or backslashes are handled safely.

The pool size is budgeted: both the GORM and XORM layers share the one remote
engine, so each layer's max-open-connections is capped at 3 unless you
configure a lower value.

## Verification

Smoke-test the full Gogs + PizzaSQL + PizzaKV startup path against already-built
engine binaries (both paths must be absolute):

```sh
PIZZASQL_BIN=/abs/path/to/pizzasql/bin/pizzasql \
PIZZAKV_BIN=/abs/path/to/pizzakv/bin/pizzakv \
go test -run TestPizzaSQLSmokeIntegration ./internal/database -count=1
```

The driver integration tests can run against a live local engine while the
launcher is running:

```sh
PIZZASQL_TEST_DSN='host=127.0.0.1 port=15432 user=gogs dbname=driver_tests sslmode=disable' \
go test -race ./internal/pizzadriver -count=1
```

Both skip silently unless their environment variables are set, so do not treat
a skip as a pass.

## Backup and restore

Backup and restore are not supported for this backend. The dump and import
paths reconstruct tables through the ORM using the original table models, which
would recreate the composite `lfs_object` primary key that this backend cannot
store correctly.

## Branding

The home page and footer of this fork identify the hosted instance:

- `templates/home.tmpl` adds a line under the hero stating the instance is hosted
  on database.pizza and stores its data in a managed PizzaSQL database.
- `templates/base/footer.tmpl` adds a "Hosted on database.pizza" link.

Both templates are embedded in the binary, but `custom/templates/` overrides them
at runtime, so a deployment can ship them without rebuilding. On the
`git.database.pizza` host they are installed under
`/home/pizzadatabase/gogs/shared/custom/templates/`.

## Status

This backend is experimental. The engine requirements above are met only by a
local source build. It runs the `git.database.pizza` instance through the managed
service; there is no claim of general production support.
