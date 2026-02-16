#!/bin/sh
set -e

# Entrypoint for pgcdc:
# - If /app/.env exists, run the binary
# - Otherwise copy .env.example -> .env (non-secret defaults) and warn the user

APP=/app/pgcdc
ENVFILE=/app/.env
EXAMPLE=/app/.env.example

echo "[entrypoint] checking .env"

if [ -f "$ENVFILE" ]; then
  echo "[entrypoint] found $ENVFILE"
else
  if [ -f "$EXAMPLE" ]; then
    echo "[entrypoint] $ENVFILE not found — copying $EXAMPLE -> $ENVFILE"
    cp "$EXAMPLE" "$ENVFILE"
    echo "[entrypoint] copied example .env; ensure you mount a real .env or provide env vars/secrets"
  else
    echo "[entrypoint] no .env found (neither $ENVFILE nor $EXAMPLE)."
    echo "[entrypoint] Provide configuration via environment variables or mount /app/.env."
  fi
fi

echo "[entrypoint] starting pgcdc"
exec "$APP" "$@"
