# TwitchRecorder

One repository with three independent parts:

- `recorder/` — Go app that checks Twitch and records live channels on the Linux server.
- `transfer/` — Windows task that copies completed recordings to a local folder.
- `listener/` — local Windows web app for playing files and tracking watched status.

The root `.env`, `recordings/`, `deploy.sh`, and `package.json` remain at the repository root so the running server's PM2 working directory and recording paths do not change.

## Recorder on Linux

Requires Go, Node.js/npm, PM2, and Streamlink. Install Streamlink using the [official instructions](https://streamlink.github.io/install). The app and deployment stop with an error when Streamlink is missing; they do not install system packages.

Copy `.env.example` to `.env` and set `BOT_LOGIN` and `BOT_OAUTH`. The OAuth token must be a Twitch user access token for `BOT_LOGIN`. The app validates it to discover its Client ID, then uses both values to call Get Streams. `.env` is ignored by Git. Restrict access to it on the server (`chmod 600 .env`).

Set `DEVELOPER_TELEGRAM_BOT_TOKEN` and `DEVELOPER_TELEGRAM_CHAT_ID` in `.env` for disk alerts. The app checks free space on the filesystem containing `OUTPUT_DIR` immediately and every minute. Below 1 GB (1,000,000,000 bytes), it sends one Telegram alert; a failed send is retried at the next check. Once free space rises to at least 1 GB, a later drop can trigger a new alert.

Set `CHANNEL_LOGINS` to comma-separated `username:quality` entries, for example `CHANNEL_LOGINS=n_y_x_official:audio_only,bcomplex_matia:best`. Each quality is passed as the Streamlink stream selector; `best`, `audio_only`, and names such as `720p60` are supported when available. A username without `:quality` defaults to `audio_only`. Repeating a username with different qualities is an error. The old `CHANNEL_LOGIN` setting still works when `CHANNEL_LOGINS` is unset. If neither is set, the app monitors `n_y_x_official` at `audio_only`.

`OUTPUT_DIR` and `POLL_SECONDS` are optional. Recordings use the channel and server-local start date in their filenames, for example `india-2024-09-09.ts`. If that file already exists, the next recording uses `india-2024-09-09-2.ts`. Streamlink writes to `.ts.part` while recording; after it exits, the app renames a nonempty file to `.ts`. An orphaned `.part` file is left for manual recovery after a crash. The app keeps daily sequence numbers in `OUTPUT_DIR/.recording-state` so moving finished files off the server does not reuse their names.

The app checks `.env` every 5 seconds. After a change remains stable for two checks, it waits until all active Streamlink recordings finish, then exits for PM2 to restart it with the new settings. A continuously running recording can delay the restart indefinitely.

From the repository root:

```bash
npm run deploy
pm2 logs twitch-recorder
```

Deployment builds `./recorder` into the root `twitch-recorder` binary, starts or restarts PM2, and saves the PM2 process list. Run `pm2 startup` once on the server for reboot startup.

## Transfer to Windows

The Windows computer needs SSH access to `root@reviewer`. Copy `transfer/transfer-config.example.json` to `transfer/transfer-config.json` and set `destination` to a folder such as `F:\\DJ` (JSON backslashes must be escaped). The config is ignored by Git and is read on every run.

Run `transfer/transfer-recordings.ps1 -DryRun` to inspect completed `.ts` files. Run `transfer/register-transfer-task.ps1` to install the `TwitchRecorderTransfer` task. It runs every two hours and at Windows startup; missed runs are retried when the computer is available. The task uses this Windows user's SSH key. Its log is `transfer/transfer.log`.

The transfer ignores `.ts.part` files. It downloads each completed `.ts` to a temporary name, compares SHA-256 hashes, renames the local copy, and asks the server to remove the file only if its hash still matches. A mismatch leaves both files for inspection. Run `transfer/transfer-recordings.ps1` manually for an immediate transfer.

## Listener on Windows

The listener is a small Go web server bound to `127.0.0.1`. It reads completed `.ts` files from the configured folder, plays AAC audio and H.264/AAC video using FFmpeg, and stores watched status in the ignored `listener/state.json`. It does not keep converted copies of the recordings. Playback controls include seeking, speed, volume, and fullscreen for video. A recording is marked watched when playback reaches the end; you can also mark one or selected files watched or unwatched, or mark the whole library watched.

Requires Go, FFmpeg, and FFprobe on the Windows user's `PATH`. FFmpeg can be installed with `scoop install ffmpeg`. Copy `listener/config.example.json` to `listener/config.json` and set `media_dir` and `listen_addr`. The default config points to `F:\\DJ` and `127.0.0.1:8787`. When changing the transfer destination, also change the listener's `media_dir`.

Run `listener/install.ps1` from PowerShell. It builds the listener, installs the `TwitchListener` scheduled task for this user's logon, and starts it immediately. Open [http://127.0.0.1:8787](http://127.0.0.1:8787) and pin that tab if desired. The task runs while this Windows user is logged in and restarts after failures. Run the install script again after code changes. Logs are in `listener/listener.log`.

Run `go test ./...` from the repository root to test both Go apps.
