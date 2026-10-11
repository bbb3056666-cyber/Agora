#!/bin/sh
set -eu

mkdir -p /app/.run/uploads
chown app:app /app/.run/uploads

exec su-exec app "$@"
