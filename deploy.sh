#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

for command_name in go pm2 streamlink; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "$command_name is required and was not found on PATH." >&2
    if [ "$command_name" = streamlink ]; then
      echo "Install Streamlink first: https://streamlink.github.io/install" >&2
    fi
    exit 1
  fi
done

if [ ! -f .env ]; then
  echo "Missing .env with BOT_LOGIN and BOT_OAUTH." >&2
  exit 1
fi
for key in DEVELOPER_TELEGRAM_BOT_TOKEN DEVELOPER_TELEGRAM_CHAT_ID; do
  if ! grep -Eq "^${key}=.+" .env; then
    echo "Missing $key in .env; add it before deploying." >&2
    exit 1
  fi
done
streamlink --version >/dev/null

echo "Building twitch-recorder..."
go build -o twitch-recorder.new .
mv -f twitch-recorder.new twitch-recorder

if pm2 describe twitch-recorder >/dev/null 2>&1; then
  pm2 restart twitch-recorder --update-env
else
  pm2 start ./twitch-recorder --name twitch-recorder --interpreter none --cwd "$PWD"
fi
pm2 save
pm2 status twitch-recorder
