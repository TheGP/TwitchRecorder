# Twitch audio recorder

Checks Twitch's Get Streams API every 30 seconds and records configured channels with Streamlink when live. Each channel has its own monitor and recording process, so simultaneous streams can be recorded. If Streamlink stops while a channel remains live, its next poll starts a new file.

## Setup

Requires Go, Node.js/npm, PM2, and Streamlink on the target machine. Install Streamlink using the [official instructions](https://streamlink.github.io/install). The app and deployment stop with an error when Streamlink is missing; they do not install system packages.

Copy `.env.example` to `.env` and set `BOT_LOGIN` and `BOT_OAUTH`. The OAuth token must be a Twitch user access token for `BOT_LOGIN`. The app validates it to discover its Client ID, then uses both values to call Get Streams. `.env` is ignored by Git. Restrict access to it on the server (`chmod 600 .env`).

The app checks `.env` contents every 5 seconds. After a change remains stable for two checks, it waits until all active Streamlink recordings finish, then exits for PM2 to restart it with the new settings. Other channels continue recording while it waits. A continuously running recording can delay the restart indefinitely. Temporary failures to read `.env` are retried. Editing `.env` does not require running `npm run deploy`.

Set `DEVELOPER_TELEGRAM_BOT_TOKEN` and `DEVELOPER_TELEGRAM_CHAT_ID` in `.env` for disk alerts. The app checks free space on the filesystem containing `OUTPUT_DIR` immediately and every minute on Linux or Windows. Below 1 GB (1,000,000,000 bytes), it sends one Telegram alert; a failed send is retried at the next check. Once free space rises to at least 1 GB, a later drop can trigger a new alert. Deployment checks for these settings before restarting PM2. Since `.env` is ignored by Git, add the Telegram settings on the server before deploying.

Set `CHANNEL_LOGINS` to comma-separated `username:quality` entries, for example `CHANNEL_LOGINS=n_y_x_official:audio_only,bcomplex_matia:best`. Each quality is passed as the Streamlink stream selector; `best`, `audio_only`, and specific names such as `720p60` are supported when available for that stream. A username without `:quality` defaults to `audio_only`. Repeating a username with different qualities is an error. The old `CHANNEL_LOGIN` setting still works when `CHANNEL_LOGINS` is unset. If neither is set, the app monitors `n_y_x_official` at `audio_only`.

`OUTPUT_DIR` and `POLL_SECONDS` are optional. Recordings use the channel and server-local start date in their filenames, for example `india-2024-09-09.ts`. If that file already exists, the next recording uses `india-2024-09-09-2.ts`, then `-3.ts`, and so on. Streamlink writes to `.ts.part` while recording; after it exits, the app renames a nonempty file to `.ts`. An orphaned `.part` file is left for manual recovery after a crash. The app keeps daily sequence numbers in `OUTPUT_DIR/.recording-state` so moving finished `.ts` files off the server does not reuse their names. Keep that state directory when cleaning recordings.

## Run

```bash
npm run deploy
pm2 logs twitch-recorder
```

Deployment builds the Go binary, starts or restarts the PM2 process, and saves the PM2 process list. Run `pm2 startup` once on the server if PM2 should start after a reboot.

## Move completed recordings to Windows

On the Windows computer with SSH access to `root@reviewer`, copy `transfer-config.example.json` to `transfer-config.json` and set `destination` to the folder where recordings should be saved (for example `F:\\DJ` in JSON). The local config is ignored by Git. The task reads it on every run, so changing the destination does not require registering the task again. Run `transfer-recordings.ps1 -DryRun` to inspect the completed `.ts` files. Run `register-transfer-task.ps1` to install the `TwitchRecorderTransfer` task. It runs every two hours and at Windows startup; missed runs are retried when the computer is available. The task uses this Windows user's SSH key, so it does not require an interactive login. Its log is `transfer.log` beside the script.

The transfer script ignores `.ts.part` files. For each completed `.ts`, it downloads to a temporary name under the configured destination, compares SHA-256 hashes, renames the local copy, then asks the server to remove the file only if its hash still matches. If a matching local file already exists, it retries only the server removal. A mismatch leaves both files for inspection. Run `transfer-recordings.ps1` manually for an immediate transfer; pass `-Destination 'D:\Recordings'` to override the config for one run.
