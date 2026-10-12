# Syncing between devices

tjek syncs through a small server you run yourself. One machine holds the
shared copy, and there is no third-party service. The same `tjek` program is
both the server and the client.

## Run a server

On the machine that should hold the shared copy (a home server reachable over
Tailscale or your LAN, for example):

```sh
tjek serve --listen 100.x.y.z:8765 --token "$(openssl rand -hex 32)"
# or: TJEK_SYNC_TOKEN=… tjek serve --listen 100.x.y.z:8765
```

A token is **required**; tjek refuses to run a server without one.
`--listen` defaults to `127.0.0.1:8765`; bind to a Tailscale or LAN address
so other devices can reach it. Tailscale already encrypts the connection;
anywhere else, give the server a certificate and it serves https itself:

```sh
tjek serve --listen 0.0.0.0:8765 --tls-cert cert.pem --tls-key key.pem
tjek sync --url https://tasks.example.com:8765 --save
```

The certificate is re-read when its files change, so one renewed in place
(`tailscale cert`, Let's Encrypt) is picked up without a restart.

To keep the server running, wrap it in a `systemd --user` unit with the token
in an `EnvironmentFile` (mode 600) and enable lingering. See
[SECURITY.md](../SECURITY.md#choosing-a-sync-token) for choosing a token.

The server keeps its own `tasks.db` and answers on:

- `POST /v1/sync`: sync (needs the token)
- `GET  /v1/health`: a liveness check
- `GET  /v1/events`: a stream that tells connected clients to pull now

## Your tasks on a phone

`--web` adds a page at the server's address for a phone's browser: your open
tasks in the order the Tasks tab shows them, a line to add one with the same
words as the app (`Buy milk #shopping due:friday`), and a circle to close one.

```sh
tjek serve --listen 100.x.y.z:8765 --web
```

Open `http://100.x.y.z:8765/` on a phone that is on the same tailnet and type
the server's token once; the browser remembers it until the token changes.
The page uses the theme and language set on the server, and a change made on
it reaches your other devices within a second, as any sync does. Add it to
the home screen and it opens like an app. If a phone that is signed in goes
missing, `tjek serve --new-token` and a restart sign every browser out; your
devices then need the new token too.

The page is only as reachable as the server, so keep it on Tailscale or a
LAN. For a name and https on a tailnet, point a DNS record at the server's
Tailscale address (with Cloudflare, as "DNS only"), get a certificate for that
name with a DNS challenge (`lego --dns cloudflare`, or certbot's Cloudflare
plugin), and pass it with `--tls-cert` and `--tls-key`.

## Point a device at it

```sh
tjek sync --url http://100.x.y.z:8765 --token "<token>" --save
```

`--save` stores the address and token, so later syncs need no flags;
`TJEK_SYNC_URL` / `TJEK_SYNC_TOKEN` work too. From then on the app syncs by
itself (at launch and exit, every few minutes, and as soon as another device
changes something), and CLI commands sync in the background. Set
`"auto_sync": false` in `sync.json` to sync only when you run `tjek sync`.

All of this is also in the app's **Settings** tab: turn auto-sync on or off,
edit the server address and token, "Sync now", or make this machine the
server.

Your own copy of the tasks always comes first: a network failure never blocks
the app.

The first sync from a device that already has tasks asks what to do with
them: add them to the shared copy (`--adopt-local`) or replace them with it
(`--adopt-remote`, which saves a backup first). It asks because neither can
be undone once the other devices have them.

A [shared project](guide.md#shared-projects) is the exception: it travels
only through its folder, so the server neither gets nor gives its tasks.

## How changes are merged

Each task is matched by its ID, and each field of it is merged on its own:
if one device changes a task's due date and another its priority, both
changes are kept. Tags are merged one by one too, so a tag added on each
device ends up on both. Only when two devices change the same field does one
edit win, the later one, and the other is written to `sync.log` in the state
directory; `tjek sync --recover` lists and restores it, putting back just the
fields that lost. Comments and time entries from both devices are kept. A
deleted task leaves a marker so the delete reaches other devices instead of
the task coming back.

"Later" means later as far as the devices know: an edit made after a device
has seen another device's edit always wins over it, even if that device's
clock is behind. Clocks should still be roughly right (any normal NTP setup
is enough), since that is what orders two edits made without either device
seeing the other. A device on an older tjek still syncs, but its changes are
merged a whole task at a time, as they used to be. Tasks and the board's
column names sync; other settings stay per device.
