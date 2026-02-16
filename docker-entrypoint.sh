#!/bin/sh
set -e

# Entrypoint for pgcdc:
# - If /app/.env exists, run the binary
# - Otherwise copy .env.example -> .env (non-secret defaults) and warn the user

APP=/app/pgcdc
ENVFILE=/app/.env
EXAMPLE=/app/.env.example

# Schema file handling
SCHEMA=/app/schema.yaml
SCHEMA_EXAMPLE=/app/schema.example.yaml

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
 
# Handle schema.yaml similarly: prefer mounted /app/schema.yaml, otherwise copy example
if [ -f "$SCHEMA" ]; then
  echo "[entrypoint] found $SCHEMA"
else
  if [ -f "$SCHEMA_EXAMPLE" ]; then
    echo "[entrypoint] $SCHEMA not found — copying $SCHEMA_EXAMPLE -> $SCHEMA"
    cp "$SCHEMA_EXAMPLE" "$SCHEMA"
    echo "[entrypoint] copied example schema; mount a real schema.yaml for production if needed"
  else
    echo "[entrypoint] no schema found (neither $SCHEMA nor $SCHEMA_EXAMPLE)."
  fi
fi

echo "[entrypoint] starting pgcdc"
exec "$APP" "$@"
