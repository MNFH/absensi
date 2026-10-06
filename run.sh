#!/usr/bin/env sh
# Loads .env into the environment and starts the bot (Linux/macOS).
set -e
cd "$(dirname "$0")"
[ -f .env ] || { echo ".env not found (copy .env.example)" >&2; exit 1; }
set -a
. ./.env
set +a
exec go run ./cmd/bot
