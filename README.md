# Twitch audio recorder

Checks Twitch's Get Streams API every 30 seconds and records configured channels with Streamlink when live. Each channel has its own monitor and recording process, so simultaneous streams can be recorded. If Streamlink stops while a channel remains live, its next poll starts a new file.

## Setup

Requires Go, Node.js/npm, PM2, and Streamlink on the target machine. Install Streamlink using the [official instructions](https://streamlink.github.io/install). The app and deployment stop with an error when Streamlink is missing; they do not install system packages.

Copy `.env.example` to `.env` and set `BOT_LOGIN` and `BOT_OAUTH`. The OAuth token must be a Twitch user access token for `BOT_LOGIN`. The app validates it to discover its Client ID, then uses both values to call Get Streams. `.env` is ignored by Git. Restrict access to it on the server (`chmod 600 .env`). Replace the token if it expires or is revoked, then restart the app.

Set `CHANNEL_LOGINS` to comma-separated Twitch usernames, for example `CHANNEL_LOGINS=n_y_x_official,bcomplex_matia`. Spaces and duplicate names are removed. The old `CHANNEL_LOGIN` setting still works for one channel when `CHANNEL_LOGINS` is unset. If neither is set, the app monitors `n_y_x_official`.

`OUTPUT_DIR` and `POLL_SECONDS` are optional. Recordings use the channel, stream ID, and UTC start time in their filenames. The output format is `.ts`, matching the existing Streamlink command.

## Run

```bash
npm run deploy
pm2 logs twitch-recorder
```

Deployment builds the Go binary, starts or restarts the PM2 process, and saves the PM2 process list. Run `pm2 startup` once on the server if PM2 should start after a reboot.
