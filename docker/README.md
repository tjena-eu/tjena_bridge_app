# tjena Bridge in Docker

The tjena personal WhatsApp bridge on a home server, NAS or Raspberry Pi
(64-bit) instead of a spare phone. It runs the same programs as the tjena
Bridge app, and a web UI replaces the app's screens:

```
mautrix-whatsapp (unmodified) ─► bridge-relay (127.0.0.1) ─► gateway (wap.tjena.eu)
wacall (WhatsApp calls, optional) ──────────────────────────► gateway
web UI :8080 ─► setup, WhatsApp linking, settings, backup, move, logs
```

WhatsApp messages are decrypted only in this container and on your tjena
devices. Like the app, it only connects out: no port needs to be open to the
internet.

## Start

You only need [`compose.yaml`](compose.yaml). Copy it to your server and run:

```bash
docker compose up -d
```

It pulls the prebuilt image `ghcr.io/tjena-eu/tjena-bridge` (amd64 and arm64,
so also Raspberry Pi 4/5 with a 64-bit OS). On a NAS UI (Portainer, Synology,
Unraid, …) create a stack from the same file.

Open `http://<server-ip>:8080` from any device in your home network and
choose a web UI password. Then:

1. **Sign in** with your tjena account. The password only goes to your tjena
   server, to create the bridge's own session ("WhatsApp bridge (Docker)").
2. **Link WhatsApp** in the WhatsApp tab: with a pairing code (enter your
   number) or by scanning a QR code with WhatsApp → Linked devices.
3. Optional: **Calls** tab → on, then "Set up calls" for each account.

### Build it yourself

From the repository root:

```bash
docker compose -f docker/compose.yaml -f docker/compose.build.yaml up -d --build
```

The first build takes a few minutes (it compiles mautrix-whatsapp from its
upstream tag, unchanged).

### Image tags

| Tag | |
|---|---|
| `latest` | Latest release (recommended) |
| `0.4.0`, `0.4` | A specific release |
| `edge` | Latest commit on `main`, may be broken |

## Already have a bridge on a phone?

Sign in here with the same tjena account. The page sees your existing bridge
and offers:

- **Move from my old device**: on the phone open tjena Bridge → Settings →
  Move to a new phone, then click **Connect without a code** here. Both show
  a 6-digit number; if it is the same, confirm on both. (Or paste the phone's
  code via *Copy code*, or upload a screenshot of its QR code.) Chats, the
  WhatsApp link, the call device and the keys move over, end-to-end
  encrypted: with the number check, the gateway can pass the data on but
  can't read or redirect it. The phone removes its copy once this side
  confirms.
- **Restore from a backup file** (`.tjbk` from the phone app or this page).
- **Start fresh** if the old phone is lost: link WhatsApp again; chats may
  show up a second time.

Moving the other way (Docker → phone, or to another Docker host) is
*Backup & move → Move to another device*: the new device scans the QR code,
takes the text, or uses *Connect without a code* and compares the number.

Only one device may run the bridge at a time.

## Settings

| Variable | Default | |
|---|---|---|
| `WEBUI_PASSWORD` | – | Fixed web UI password. If unset, you choose one on the first visit (stored as a PBKDF2 hash). |
| `WEBUI_ALLOW` | `private` | Who may open the web UI: `private` (this machine, home networks 10/8, 172.16/12, 192.168/16, Tailscale/CGNAT 100.64/10, IPv6 ULA and link-local), `any`, or a list like `192.168.1.0/24, fd00::/8`. Others get "403". |
| `WEBUI_LISTEN` | `:8080` | Web UI address inside the container. |
| `TJENA_DATA` | `/data` | Volume: `secrets.json` (tokens and keys), `bridge/` (databases, logs). |
| `TJENA_RUN` | `/tmp/tjena-run` | Generated configs, removed ~15 s after each start. Use a tmpfs. |
| `TZ` | – | Time zone for logs. |

Everything else (history import, WhatsApp status, extra users, gateway) is in
the web UI.

## Security

- The web UI can link WhatsApp, so protect it like a WhatsApp login. It is
  open to your home network on port 8080, but only answers devices with a
  private address (`WEBUI_ALLOW`). **Don't forward port 8080 on your
  router.** For access from outside, use a VPN (Tailscale, WireGuard) or a
  reverse proxy with HTTPS and its own access control (with a proxy in
  front, every visitor looks like the proxy's address).
- Whoever opens the page first chooses the password, so do it right after
  the first start, or set `WEBUI_PASSWORD`. Choosing it only works when the
  page is opened by IP address or a local name (`nas`, `*.local`, `*.lan`,
  `*.home.arpa`, `*.fritz.box`, …), so a website can't claim it through DNS
  rebinding. Behind a reverse proxy with a real domain, set `WEBUI_PASSWORD`.
- On plain HTTP the password crosses your home network unencrypted, like
  most router and NAS pages. Use a reverse proxy with HTTPS if that matters.
- The volume holds the bridge tokens and the encryption keys in plain files
  (mode 0600, owner uid 1000). On the phone they're in the Android Keystore;
  here the protection is the host's. Encrypt the disk if the machine isn't
  yours alone, and treat backups of the volume as secret.
- The container runs as a normal user, read-only, without capabilities.

## Calls

Calls work through the tjena TURN server, so the default Docker network is
fine. For a more direct audio path you can use `network_mode: host` (then
remove `ports:`; the web UI is on port 8080 of the host).

## Backups

*Backup & move → Backup* downloads an encrypted `.tjbk` file (same format as
the phone app). Or back up the `tjena-bridge-data` volume while the container
is stopped.

## Update

```bash
docker compose pull && docker compose up -d
```

(or with the build overlay: `git pull`, then the build command again).

The bridge database stays in the volume. To pin another mautrix-whatsapp
version, build with `--build-arg MAUTRIX_TAG=v0.xxxx.x` (keep it equal to
`PINNED_TAG` in `android-binary/build.sh`).

## Troubleshooting

- *403 "this address may not open the web UI"*: your device's address isn't
  private, e.g. Docker with IPv6 NAT or an unusual network. Add it to
  `WEBUI_ALLOW`.
- `docker logs tjena-bridge`: the same (redacted) output as the Logs tab.
- *Stopped by the server*: another device runs the bridge now. Settings →
  *Make this the active bridge* takes it back (the other device stops).
- Forgot the web UI password: stop the container and delete `pass_salt` and
  `pass_hash` from `/data/manager.json` in the volume, or set `WEBUI_PASSWORD`.

## License

AGPL-3.0-or-later, like the phone app. mautrix-whatsapp is AGPL-3.0;
WhatsApp calls use meowcaller (MIT, see `wacall/THIRD_PARTY.md`). Licence
texts are in the image under `/usr/share/doc/tjena-bridge/`.
