#!/bin/sh
set -e

# Entrypoint for pgcdc:
# - If /app/.env exists, run the binary
# - Otherwise warn the user

APP=/app/pgcdc
SCHEMA=/app/schema.yaml

# Handle schema.yaml: just warn if missing
if [ -f "$SCHEMA" ]; then
  echo "[entrypoint] found $SCHEMA"
else
  echo "[entrypoint] WARNING: $SCHEMA not found. Provide configuration by mounting /app/schema.yaml if needed."
fi

echo "[entrypoint] starting pgcdc"
exec "$APP" "$@"
