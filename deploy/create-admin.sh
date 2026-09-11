#!/bin/sh
# Create the initial Gogs administrator through the CLI.
#
# The web server must have been started once first so that it creates the
# database schema; "admin create-user" only opens a connection and does not run
# migrations. Run as the gogs user or with an equivalent environment, for
# example: sudo -u gogs ./deploy/create-admin.sh ...

set -eu

GOGS_BIN=${GOGS_BIN:-/home/pizzadatabase/gogs/current/gogs}
GOGS_HOME=${GOGS_HOME:-/home/pizzadatabase/gogs/shared}
GOGS_WORK_DIR=${GOGS_WORK_DIR:-/home/pizzadatabase/gogs/current}
GOGS_CUSTOM=${GOGS_CUSTOM:-/home/pizzadatabase/gogs/shared/custom}
GOGS_CONF=${GOGS_CONF:-${GOGS_CUSTOM}/conf/app.ini}

usage() {
	echo "usage: $0 --name <username> --email <email> --password-file <path> [--admin]"
	exit 2
}

name=
email=
password_file=
admin=0

while [ $# -gt 0 ]; do
	case "$1" in
	--name) name=$2; shift 2 ;;
	--email) email=$2; shift 2 ;;
	--password-file) password_file=$2; shift 2 ;;
	--admin) admin=1; shift ;;
	*) usage ;;
	esac
done

[ -n "$name" ] || usage
[ -n "$email" ] || usage
[ -n "$password_file" ] || usage

export HOME=$GOGS_HOME
export GOGS_WORK_DIR
export GOGS_CUSTOM

set -- admin create-user --config "$GOGS_CONF" --name "$name" --email "$email" --password-file "$password_file"
if [ "$admin" -eq 1 ]; then
	set -- "$@" --admin
fi

exec "$GOGS_BIN" "$@"
