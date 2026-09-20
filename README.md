# Twitch audio recorder

Checks Twitch's Get Streams API every 30 seconds and records configured channels with Streamlink when live. Each channel has its own monitor and recording process, so simultaneous streams can be recorded. If Streamlink stops while a channel remains live, its next poll starts a new file.

## Setup

Requires Go, Node.js/npm, PM2, and Streamlink on the target machine. Install Streamlink using the [official instructions](https://streamlink.github.io/install). The app and deployment stop with an error when Streamlink is missing; they do not install system packages.

Copy `.env.example` to `.env` and set `BOT_LOGIN` and `BOT_OAUTH`. The OAuth token must be a Twitch user access token for `BOT_LOGIN`. The app validates it to discover its Client ID, then uses both values to call Get Streams. `.env` is ignored by Git. Restrict access to it on the server (`chmod 600 .env`). Replace the token if it expires or is revoked, then restart the app.

Set `DEVELOPER_TELEGRAM_BOT_TOKEN` and `DEVELOPER_TELEGRAM_CHAT_ID` in `.env` for disk alerts. The app checks free space on the filesystem containing `OUTPUT_DIR` immediately and every minute on Linux or Windows. Below 1 GB (1,000,000,000 bytes), it sends one Telegram alert; a failed send is retried at the next check. Once free space rises to at least 1 GB, a later drop can trigger a new alert. Deployment checks for these settings before restarting PM2. Since `.env` is ignored by Git, add the Telegram settings on the server before deploying.

Set `CHANNEL_LOGINS` to comma-separated `username:quality` entries, for example `CHANNEL_LOGINS=n_y_x_official:audio_only,bcomplex_matia:best`. Each quality is passed as the Streamlink stream selector; `best`, `audio_only`, and specific names such as `720p60` are supported when available for that stream. A username without `:quality` defaults to `audio_only`. Repeating a username with different qualities is an error. The old `CHANNEL_LOGIN` setting still works when `CHANNEL_LOGINS` is unset. If neither is set, the app monitors `n_y_x_official` at `audio_only`.

`OUTPUT_DIR` and `POLL_SECONDS` are optional. Recordings use the channel and server-local start date in their filenames, for example `india-2024-09-09.ts`. If that file already exists, the next recording uses `india-2024-09-09-2.ts`, then `-3.ts`, and so on. The output format is `.ts`, matching the existing Streamlink command.

## Run

```bash
npm run deploy
pm2 logs twitch-recorder
```

Deployment builds the Go binary, starts or restarts the PM2 process, and saves the PM2 process list. Run `pm2 startup` once on the server if PM2 should start after a reboot.
