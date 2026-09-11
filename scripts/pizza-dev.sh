#!/usr/bin/env bash
# pizza-dev: launch a disposable local Gogs + PizzaSQL + PizzaKV stack.
#
# Local development only. This script builds Gogs and PizzaSQL into a local
# directory (default .pizza-dev/), launches PizzaKV, PizzaSQL (PostgreSQL
# wire), and Gogs web all bound to 127.0.0.1, and keeps every artifact
# (binaries, database, repositories, logs, and config) under that directory.
# It never touches production or containers and accepts no real TLS
# credentials. PizzaKV must already be built (run "make build" in ../pizzakv);
# Gogs and PizzaSQL are built on demand unless their flags point at existing
# binaries. Use --rebuild after pulling engine or installer fixes so a cached
# binary never hides them.
set -euo pipefail

die() {
  echo "pizza-dev: $*" >&2
  exit 1
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SIBLING_DIR="$(cd "$REPO_ROOT/.." && pwd)"

# Loopback bind address and default ports, overridable via environment.
BIND="127.0.0.1"
HTTP_PORT="${PIZZA_DEV_HTTP_PORT:-3000}"
PG_PORT="${PIZZA_DEV_PG_PORT:-15432}"
KV_PORT="${PIZZA_DEV_KV_PORT:-16379}"

# Every artifact lives under the data directory. It defaults to the gitignored
# .pizza-dev/ in the Gogs repo root, or an absolute PIZZA_DEV_DIR for isolated
# test runs.
case "${PIZZA_DEV_DIR:-}" in
  '')
    DATA_DIR="$REPO_ROOT/.pizza-dev"
    ;;
  /*)
    DATA_DIR="$PIZZA_DEV_DIR"
    ;;
  *)
    die "PIZZA_DEV_DIR must be an absolute path, got: $PIZZA_DEV_DIR"
    ;;
esac
BIN_DIR="$DATA_DIR/bin"
LOG_DIR="$DATA_DIR/log"
CONF_DIR="$DATA_DIR/custom/conf"
CONF_FILE="$CONF_DIR/app.ini"
KV_PATH="$DATA_DIR/kv/gogs.pkvdb"
APP_DATA_DIR="$DATA_DIR/data"
REPO_DIR="$DATA_DIR/repos"

GOGS_BIN=""
PIZZASQL_BIN=""
PIZZAKV_BIN=""
REBUILD=false

usage() {
  cat <<EOF
Usage: $0 [options]

Run Gogs locally against an owned PizzaKV + PizzaSQL stack bound to $BIND.

Options:
  -gogs-bin PATH      Use this Gogs binary instead of building one.
  -pizzasql-bin PATH  Use this PizzaSQL binary instead of building one.
  -pizzakv-bin PATH   Use this PizzaKV binary (default: ../pizzakv/bin/pizzakv).
  --rebuild           Rebuild Gogs and PizzaSQL even if binaries exist. Use this
                      after pulling changes so a stale cached binary is never used.
  -h, --help          Show this help.

Environment:
  PIZZA_DEV_DIR        Absolute directory for all data (default: .pizza-dev).
  PIZZA_DEV_HTTP_PORT  Gogs web port (default: 3000).
  PIZZA_DEV_PG_PORT    PizzaSQL PostgreSQL wire port (default: 15432).
  PIZZA_DEV_KV_PORT    PizzaKV port (default: 16379).

This is local-only and must never be used for production.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    -gogs-bin)
      [ "$#" -ge 2 ] || die "-gogs-bin requires a path"
      GOGS_BIN="$2"
      shift 2
      ;;
    -pizzasql-bin)
      [ "$#" -ge 2 ] || die "-pizzasql-bin requires a path"
      PIZZASQL_BIN="$2"
      shift 2
      ;;
    -pizzakv-bin)
      [ "$#" -ge 2 ] || die "-pizzakv-bin requires a path"
      PIZZAKV_BIN="$2"
      shift 2
      ;;
    --rebuild)
      REBUILD=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1 (run with --help)"
      ;;
  esac
done

command -v go >/dev/null 2>&1 || die "go is required but not found in PATH"
command -v git >/dev/null 2>&1 || die "git is required but not found in PATH"

# Ports must be numeric, in range, and distinct from one another.
is_valid_port() {
  case "$1" in
    ''|*[!0-9]*) return 1 ;;
  esac
  [ "$1" -ge 1 ] 2>/dev/null && [ "$1" -le 65535 ] 2>/dev/null
}

is_valid_port "$HTTP_PORT" || die "PIZZA_DEV_HTTP_PORT=$HTTP_PORT is not a valid port"
is_valid_port "$PG_PORT" || die "PIZZA_DEV_PG_PORT=$PG_PORT is not a valid port"
is_valid_port "$KV_PORT" || die "PIZZA_DEV_KV_PORT=$KV_PORT is not a valid port"
if [ "$HTTP_PORT" = "$PG_PORT" ] || [ "$HTTP_PORT" = "$KV_PORT" ] || [ "$PG_PORT" = "$KV_PORT" ]; then
  die "ports must be distinct (HTTP=$HTTP_PORT PG=$PG_PORT KV=$KV_PORT)"
fi

# Keep everything private: the config gains a secret key after installation.
umask 077
mkdir -p "$BIN_DIR" "$LOG_DIR" "$CONF_DIR" "$(dirname "$KV_PATH")" \
  "$APP_DATA_DIR" "$REPO_DIR"

# Read a single key from a section of the existing config so a changed port can
# be detected without rewriting (or echoing back) anything the installer wrote.
config_value() {
  local section="$1" key="$2" file="$3"
  awk -F= -v sec="$section" -v key="$key" '
    /^[[:space:]]*\[/ { in_sec = (index($0, "[" sec "]") > 0); next }
    in_sec {
      k = $1; gsub(/^[[:space:]]+|[[:space:]]+$/, "", k)
      if (k == key) {
        v = $2; gsub(/^[[:space:]]+|[[:space:]]+$/, "", v)
        sub(/[[:space:]]*;.*$/, "", v)
        gsub(/[[:space:]]+$/, "", v)
        print v
        exit 0
      }
    }
  ' "$file"
}

last_host_port() {
  printf '%s' "$1" | awk -F: '{print $NF}'
}

# A completed install lives in the config file; never rewrite it. If the
# requested ports drifted from what the config records, fail with guidance.
if [ -f "$CONF_FILE" ]; then
  cfg_http="$(config_value server HTTP_PORT "$CONF_FILE")"
  cfg_host="$(config_value database HOST "$CONF_FILE")"
  cfg_pg="$(last_host_port "$cfg_host")"
  if [ -n "$cfg_http" ] && [ "$cfg_http" != "$HTTP_PORT" ]; then
    die "existing config at $CONF_FILE uses HTTP_PORT=$cfg_http but PIZZA_DEV_HTTP_PORT=$HTTP_PORT; reuse the recorded port or remove $DATA_DIR"
  fi
  if [ -n "$cfg_pg" ] && [ "$cfg_pg" != "$PG_PORT" ]; then
    die "existing config at $CONF_FILE points PizzaSQL at port $cfg_pg but PIZZA_DEV_PG_PORT=$PG_PORT; reuse the recorded port or remove $DATA_DIR"
  fi
fi

# Port checks run before anything is launched so conflicts surface immediately.
port_open() {
  (exec 3<>"/dev/tcp/$1/$2") 2>/dev/null
}

check_port_free() {
  if port_open "$BIND" "$1"; then
    die "port $1 ($2) is already in use; override it with the matching PIZZA_DEV_*_PORT variable"
  fi
}

check_port_free "$HTTP_PORT" "Gogs web"
check_port_free "$PG_PORT" "PizzaSQL PostgreSQL wire"
check_port_free "$KV_PORT" "PizzaKV"

# PizzaKV is required to already be built; it is never built here.
if [ -z "$PIZZAKV_BIN" ]; then
  PIZZAKV_BIN="$SIBLING_DIR/pizzakv/bin/pizzakv"
fi
[ -x "$PIZZAKV_BIN" ] || die "PizzaKV binary not found at $PIZZAKV_BIN; run 'make build' in ../pizzakv or pass -pizzakv-bin"

# Gogs and PizzaSQL are built into the data directory unless a binary was provided.
build_gogs() {
  echo "pizza-dev: building Gogs..."
  (cd "$REPO_ROOT" && go build -o "$BIN_DIR/gogs" .) || die "failed to build Gogs"
}

build_pizzasql() {
  local src="$SIBLING_DIR/pizzasql"
  [ -d "$src" ] || die "PizzaSQL source not found at $src"
  echo "pizza-dev: building PizzaSQL..."
  (cd "$src" && go build -o "$BIN_DIR/pizzasql" .) || die "failed to build PizzaSQL"
}

if [ -z "$GOGS_BIN" ]; then
  GOGS_BIN="$BIN_DIR/gogs"
  if [ "$REBUILD" = true ] || [ ! -x "$GOGS_BIN" ]; then
    build_gogs
  fi
fi
[ -x "$GOGS_BIN" ] || die "Gogs binary not found at $GOGS_BIN"

if [ -z "$PIZZASQL_BIN" ]; then
  PIZZASQL_BIN="$BIN_DIR/pizzasql"
  if [ "$REBUILD" = true ] || [ ! -x "$PIZZASQL_BIN" ]; then
    build_pizzasql
  fi
fi
[ -x "$PIZZASQL_BIN" ] || die "PizzaSQL binary not found at $PIZZASQL_BIN"

# Write the initial custom config exactly once so a completed install (which
# rewrites this file with INSTALL_LOCK=true and a secret key) is never clobbered.
if [ ! -f "$CONF_FILE" ]; then
  RUN_USER="$(id -un)"
  cat > "$CONF_FILE" <<EOF
BRAND_NAME = Gogs
RUN_USER = $RUN_USER
RUN_MODE = dev

[server]
DOMAIN = localhost
HTTP_ADDR = $BIND
HTTP_PORT = $HTTP_PORT
PROTOCOL = http
EXTERNAL_URL = http://localhost:$HTTP_PORT/
LOCAL_ROOT_URL = http://$BIND:$HTTP_PORT/
DISABLE_SSH = true
OFFLINE_MODE = true
APP_DATA_PATH = $APP_DATA_DIR

[repository]
ROOT = $REPO_DIR

[database]
TYPE = pizzasql
HOST = $BIND:$PG_PORT
NAME = gogs
USER = gogs
PASSWORD =
SSL_MODE = disable
MAX_OPEN_CONNS = 3
MAX_IDLE_CONNS = 3

[security]
INSTALL_LOCK = false

[session]
PROVIDER = memory
PROVIDER_CONFIG = $APP_DATA_DIR/sessions

[picture]
AVATAR_UPLOAD_PATH = $APP_DATA_DIR/avatars
REPOSITORY_AVATAR_UPLOAD_PATH = $APP_DATA_DIR/repo-avatars

[attachment]
PATH = $APP_DATA_DIR/attachments

[lfs]
OBJECTS_PATH = $APP_DATA_DIR/lfs-objects
OBJECTS_TEMP_PATH = $APP_DATA_DIR/tmp/lfs-objects

[log]
MODE = console
ROOT_PATH = $LOG_DIR
LEVEL = Info
EOF
  echo "pizza-dev: wrote initial config to $CONF_FILE"
else
  echo "pizza-dev: using existing config at $CONF_FILE"
fi

KV_PID=""
SQL_PID=""
GOGS_PID=""

stop_pid() {
  local name="$1" pid="$2" waited=0
  [ -n "$pid" ] || return 0
  kill -0 "$pid" 2>/dev/null || return 0
  echo "pizza-dev: stopping $name (pid $pid)"
  kill -TERM "$pid" 2>/dev/null || true
  while kill -0 "$pid" 2>/dev/null; do
    waited=$((waited + 1))
    [ "$waited" -ge 50 ] && { kill -KILL "$pid" 2>/dev/null || true; break; }
    sleep 0.1
  done
}

on_exit() {
  # Shut down Gogs first, then PizzaSQL, then PizzaKV, never a broad kill.
  stop_pid "Gogs" "$GOGS_PID"
  stop_pid "PizzaSQL" "$SQL_PID"
  stop_pid "PizzaKV" "$KV_PID"
}

trap 'exit 130' INT
trap 'exit 143' TERM
trap on_exit EXIT

wait_for_port() {
  local host="$1" port="$2" name="$3" timeout="${4:-30}" pid="$5"
  local deadline=$(( $(date +%s) + timeout ))
  echo "pizza-dev: waiting for $name on $host:$port ..."
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if [ -n "$pid" ] && ! kill -0 "$pid" 2>/dev/null; then
      return 1
    fi
    if port_open "$host" "$port"; then
      return 0
    fi
    sleep 0.2
  done
  return 1
}

echo "pizza-dev: starting PizzaKV on $BIND:$KV_PORT"
"$PIZZAKV_BIN" -host="$BIND" -port="$KV_PORT" -path="$KV_PATH" \
  >"$LOG_DIR/pizzakv.log" 2>&1 &
KV_PID=$!
wait_for_port "$BIND" "$KV_PORT" "PizzaKV" 30 "$KV_PID" || die "PizzaKV failed to start; see $LOG_DIR/pizzakv.log"

echo "pizza-dev: starting PizzaSQL on $BIND:$PG_PORT"
"$PIZZASQL_BIN" -y -quiet -kvaddr="$BIND:$KV_PORT" -db=gogs -pool=16 \
  -pg -pg-host="$BIND" -pg-port="$PG_PORT" \
  >"$LOG_DIR/pizzasql.log" 2>&1 &
SQL_PID=$!
wait_for_port "$BIND" "$PG_PORT" "PizzaSQL" 30 "$SQL_PID" || die "PizzaSQL failed to start; see $LOG_DIR/pizzasql.log"

echo "pizza-dev: starting Gogs web"
GOGS_WORK_DIR="$REPO_ROOT" GOGS_CUSTOM="$DATA_DIR/custom" \
  "$GOGS_BIN" web --config "$CONF_FILE" \
  >"$LOG_DIR/gogs.log" 2>&1 &
GOGS_PID=$!
wait_for_port "$BIND" "$HTTP_PORT" "Gogs" 30 "$GOGS_PID" || die "Gogs failed to start; see $LOG_DIR/gogs.log"

echo ""
echo "PizzaKV   $BIND:$KV_PORT   (data: $KV_PATH)"
echo "PizzaSQL  $BIND:$PG_PORT   (PostgreSQL wire)"
echo "Gogs      http://localhost:$HTTP_PORT"
echo ""
echo "Open http://localhost:$HTTP_PORT to finish installation and create the"
echo "admin account. Logs are under $LOG_DIR. Press Ctrl-C to stop the stack."
echo ""

while :; do
  for pid in "$KV_PID" "$SQL_PID" "$GOGS_PID"; do
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "" >&2
      echo "pizza-dev: a process (pid $pid) exited unexpectedly; shutting down" >&2
      echo "pizza-dev: see $LOG_DIR for logs" >&2
      exit 1
    fi
  done
  sleep 1
done
