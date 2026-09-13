#!/bin/sh
# Hosting platforms mount the data volume as root, so SQLite cannot create its
# file under the unprivileged user. Fix ownership while we still are root, then
# drop privileges. When the platform already starts us as a non-root user the
# directory is expected to be writable and we exec straight away.
set -e

if [ "$(id -u)" = "0" ]; then
	dir=$(dirname "${DB_PATH:-/data/vkurilke.db}")
	# A read-only mount stays broken either way; let the app report the path.
	mkdir -p "$dir" 2>/dev/null || true
	chown -R app:app "$dir" 2>/dev/null || true
	exec su-exec app:app /usr/local/bin/vkurilke "$@"
fi

exec /usr/local/bin/vkurilke "$@"
