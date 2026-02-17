#!/bin/sh
set -e

# Entrypoint for pgcdc:
# - If /app/.env exists, run the binary
# - Otherwise warn the user

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
  echo "[entrypoint] WARNING: $ENVFILE not found. Provide configuration via environment variables or mount /app/.env."
fi
 
# Handle schema.yaml: just warn if missing, do not copy at runtime
if [ -f "$SCHEMA" ]; then
  echo "[entrypoint] found $SCHEMA"
else
  echo "[entrypoint] WARNING: $SCHEMA not found. Provide configuration by mounting /app/schema.yaml if needed."
fi

echo "[entrypoint] starting pgcdc"
exec "$APP" "$@"
