# Personal WhatsApp bridge: M0 spike results

Branch `detached_local_WA_bridge`, 2026-10-21. Answers the six Step 0 questions
from the agent brief, records the transport decision, and lists the changes to
the plan.

> **Corrections after the end-to-end test (same day).** The full stack was then
> built and tested against a throwaway Synapse 1.153 (`itest/run.sh`).
> That test changed three M0 answers:
>
> 1. **Item 6 was wrong.** Synapse refuses `/sync` for appservice users
>    (`handlers/sync.py`: "We no longer support AS users using /sync
>    directly"). End-to-bridge encryption therefore **requires
>    `encryption.appservice: true`** plus `experimental_features:
>    msc2409_to_device_messages_enabled, msc3202_transaction_extensions` in
>    `homeserver.yaml`, and `org.matrix.msc3202: true` in each registration.
>    MSC4190 and `self_sign` work as expected.
> 2. **`homeserver.websocket_proxy` is silently dropped** by the bridge's
>    config upgrader, because the example config doesn't contain the key. So
>    the relay is now the bridge's `homeserver.address`: it forwards both the
>    websocket and all client-API calls to the gateway.
> 3. **The design moved to a slot pool with token swap** (no Synapse restart
>    per bridge; sign in with tjena instead of a pairing code). The proxy
>    became the gateway ([tjena_proxy](https://github.com/tjena-eu/tjena_proxy)); see its `docs/server.md`.
>
> The sections below are the original M0 notes; where they disagree with the
> list above, the list above is correct.

Versions checked:

| Component | Version |
|---|---|
| mautrix-whatsapp (pinned) | `v0.2610.0` (newest tag, uses mautrix-go `v0.32.0`) |
| Shared server bridge (running) | `v26.05+dev.c97a1c4b` |
| Synapse (`tjena_matrix`) | `1.153.0` |
| mautrix-wsproxy upstream | last commit 2022-04-18 (`a5dd5f8`), built on mautrix-go `v0.9.22` |

## Summary

| # | Question | Answer | Plan change? |
|---|---|---|---|
| 1 | Can bridgev2 use wsproxy? | **Bridge side: yes.** Upstream **wsproxy: no**. It drops the fields encryption needs. | Yes: we ship our own small proxy, `wsproxy`. |
| 2 | Android build | Builds unmodified with **CGO + NDK** (`-tags goolm`). Pure Go is not possible without source changes. | No |
| 3 | Exec from app | Spike APK built (`spike-exec-app`). **Not yet run on a phone.** | Pending device test |
| 4 | Pairing-code login | Yes: flow `phone` in provisioning API v3. **But in websocket mode the API is not served on localhost.** | Yes: the bridge app runs a local relay (below) |
| 5 | Synapse hot reload | No. SIGHUP reloads only logging, TLS and caches. Each new bridge needs a restart. | No (as expected) |
| 6 | Synapse version / MSC4190 | 1.153 ≥ 1.141: `io.element.msc4190: true` in the registration is enough. Use `/sync` crypto mode, so no experimental flags are needed. | Small: pin `encryption.appservice: false` |

**Transport decision: keep the wsproxy design (Component A), but run
`tjena-wsproxy` instead of `dock.mau.dev/mautrix/wsproxy`.** No reverse tunnel
is needed, and the bridge binary stays unmodified.

---

## 1. Websocket support in bridgev2

The bridge supports it (mautrix-go `v0.32.0`):

- `bridgev2/bridgeconfig/homeserver.go`: `homeserver.websocket`,
  `homeserver.websocket_proxy`, `homeserver.ping_interval_seconds`.
- `bridgev2/matrix/connector.go:200`: if either is set, the bridge opens
  `<websocket_proxy or address>/_matrix/client/unstable/fi.mau.as_sync`
  with `Authorization: Bearer <as_token>` and `X-Mautrix-Websocket-Version: 3`,
  and does **not** start its HTTP listener.
- In websocket mode it skips the homeserver→bridge ping at startup
  (`connector.go:430`), so Synapse never needs to reach the phone.
- It reconnects with backoff. A `4001 conn_replaced` close makes the bridge
  exit, which stops two instances fighting over one registration.

**Upstream wsproxy breaks end-to-bridge encryption.** `transaction.go`
decodes every transaction into mautrix-go v0.9.22's `appservice.Transaction`
and re-encodes it. That 2021 struct has no `to_device` /
`de.sorunome.msc2409.to_device` or `device_unused_fallback_key_types` fields,
and its OTK-count type predates the per-device map. Those fields are
**silently dropped**, so room keys would never reach the bridge. Other
problems with it:

- Synapse gets 200 as soon as the frame is written, before the bridge has
  processed it, so events are lost if the socket dies.
- Other appservice calls (`/_matrix/app/v1/ping`, user/room queries) return 404.
- Config is static, so every new bridge needs a proxy restart.
- The repo has had no activity since 2022.

**Fix:** `wsproxy` (`tjena-wsproxy`, about 450 lines of Go).
It uses the same websocket protocol, and:

- forwards transaction JSON verbatim (only adds `command`, `id`, `txn_id`)
- answers Synapse only after the bridge acknowledges the transaction; when
  the bridge is offline it returns 502 and Synapse queues and retries
- relays other `/_matrix/app/*` calls with the bridge's `http_proxy` command
- calls `POST /_matrix/client/v1/appservice/{id}/ping` when a bridge connects.
  Synapse then force-retries its queue instead of waiting out a backoff of up
  to 8.5 minutes (`synapse/appservice/scheduler.py`, `appservice_ping.py:92`)
- loads bridges from `bridge_dir` (one file per bridge) and reloads every 5 s
  or on SIGHUP. Revoking a file closes that bridge's socket immediately
- sends websocket keepalive pings every 30 s (Cloudflare drops idle sockets
  after about 100 s)
- serves `GET /_tjena/status` (connected yes/no per bridge) for the
  provisioner and the tjena app status
- logs only bridge ids, transaction ids and counts, never content

`proxy_test.go` runs mautrix-go v0.32's real appservice websocket client
against it. Message and to-device events arrive, ping relays, bad tokens are
rejected, and revoke cuts delivery. It passes with `-race`.

## 2. Building for Android

- `mxmain/main.go` imports `github.com/mattn/go-sqlite3` directly, so it is
  **CGO-only**. A pure-Go SQLite driver would need a source change, which the
  brief rules out. Postgres (`lib/pq`) is also compiled in but not useful on
  a phone.
- Olm: build tag `goolm` selects the pure-Go implementation
  (`crypto/registergoolm.go`), so no libolm is needed.
- CGO on Android also gives the bionic DNS resolver. Pure Go would read
  `/etc/resolv.conf`, which Android doesn't have.

`android-binary/build.sh [tag]` clones the tag, refuses to
build a modified tree, and builds with NDK clang for API 29:

```
CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC=aarch64-linux-android29-clang \
  go build -tags goolm,sqlite_omit_load_extension ./cmd/mautrix-whatsapp
```

Result: `out/v0.2610.0/libmautrixwhatsapp.so`, 34 MB, ELF aarch64 PIE,
interpreter `/system/bin/linker64`. Needed libraries: only `libc`, `libm`,
`libdl` and `liblog`. Upgrading means changing `PINNED_TAG` and rebuilding.

A Linux build of the same tag ran the full sequence (generate config,
generate registration, start). It migrated SQLite and reached
`matrix.tjena.eu`, ending with the expected *"as_token was not accepted"*
because that test registration is not installed.

Optional external tools: `ffmpeg` (GIF→MP4, some voice/video conversions) and
`lottieconverter` (animated stickers). The bridge checks
`ffmpeg.Supported()` and turns those features off if the tool is missing.
**M2 follow-up:** bundle an arm64 `ffmpeg` as `libffmpeg.so` and put a
symlink named `ffmpeg` on the bridge's `PATH`.

## 3. Running the binary from an app

`spike-exec-app` is a minimal Kotlin app (no Flutter), minSdk
29, arm64 only:

- ships the binary as `jniLibs/arm64-v8a/libmautrixwhatsapp.so`
- sets `packaging.jniLibs.useLegacyPackaging = true`, which gives
  `extractNativeLibs=true`, so the file lands in `nativeLibraryDir`
- sets `keepDebugSymbols` so Gradle doesn't strip it again
- on launch, runs `--version`, `-e`, `-g` and then the bridge for 25 s from
  `nativeLibraryDir`, with `filesDir/bridge` as the working directory. Output
  goes to the screen and to logcat tag `BridgeSpike`

**Pass criterion:** the output reaches *"The as_token was not accepted"* and
`bridge.db` exists. That shows exec, cgo SQLite, DNS and TLS all work on the
device.

**Status: APK built, not yet run on a real phone** (no device was connected).
To run it:

```
cd spike-exec-app && ./gradlew assembleDebug
adb install -r app/build/outputs/apk/debug/app-debug.apk
adb shell am start -n eu.tjena.bridgespike/.MainActivity
adb logcat -s BridgeSpike
```

Risk to watch in M2: Android 12+ "phantom process" limits. A child process of
an app with a foreground service is allowed, but needs testing in the 48 h
run.

## 4. WhatsApp pairing-code login

mautrix-whatsapp `pkg/connector/login.go` offers flows `qr` and `phone`.
Provisioning API v3 (`bridgev2/matrix/provisioning.go`), mounted under
`/_matrix/provision`:

| Step | Request |
|---|---|
| list flows | `GET /v3/login/flows?user_id=<owner>` |
| start | `POST /v3/login/start/phone?user_id=<owner>` → step `user_input`, field `phone_number` |
| submit number | `POST /v3/login/step/{process}/{step}/user_input` `{"phone_number":"+49…"}` → step `display_and_wait`, `type: code`, `data: <pairing code>` |
| wait | `POST /v3/login/step/{process}/{step}/display_and_wait` (long-polls until `complete`) |
| others | `GET /v3/logins`, `POST /v3/logout/{loginID}`, `POST /v3/login/cancel/{process}` |

Auth: `Authorization: Bearer <provisioning.shared_secret>` (at least 16
characters, otherwise the API is disabled) plus `?user_id=`. That user needs
`login` permission, so the owner is admin.

**Catch:** the provisioning API is registered on the appservice HTTP router,
and in websocket mode **that router has no listener**. The API is only
reachable through `http_proxy` commands on the websocket, which come from the
VPS side. So `provisioning: 127.0.0.1 only` from the brief is impossible with
an unmodified bridge.

**Plan change:** the bridge app's native service runs a **local relay** on
`127.0.0.1:<random port>`:

```
bridge --ws--> 127.0.0.1 relay (bridge app) --wss--> wap.tjena.eu (tjena-wsproxy) --> Synapse
                     ^
                     └─ UI provisioning calls injected as http_proxy commands (own id range)
```

- Bridge config: `homeserver.websocket_proxy: http://127.0.0.1:<port>`. The
  bridge's other HTTP calls still go straight to `https://matrix.tjena.eu`.
- The relay passes frames through unchanged and only answers requests it
  sent itself (request ids ≥ 2^40). Phone number, pairing code and shared
  secret never leave the phone.
- Side benefit: the "proxy connected" status in the UI comes straight from
  the relay.

## 5. Adding appservices without restarting Synapse

Synapse 1.153 reads `app_service_config_files` only at startup
(`synapse/config/appservice.py: load_appservices`). Its SIGHUP handlers only
reload logging, TLS certificates and cache config. There is no admin API for
appservices. **Each new or revoked personal bridge needs a Synapse restart**,
so the provisioner's restart policy (immediate, nightly or after approval)
stays.

## 6. Synapse version and MSC4190

Running version **1.153.0**:

- MSC4190 is opt-in per registration with `io.element.msc4190: true`
  (`config/appservice.py:191`). Synapse needs no flag.
- The device-masquerading part of MSC3202 (`org.matrix.msc3202.device_id`) is
  always on (`api/auth/base.py:320`).
- The **transaction extensions** (`msc3202_transaction_extensions`,
  `msc2409_to_device_messages_enabled`) are still experimental and **not
  enabled** in `homeserver.yaml`. So the bridge must use the default
  `encryption.appservice: false`: it gets to-device events and OTK counts
  through `/sync` long-polling over normal HTTPS. That is also more robust
  over the websocket.
- To confirm in M1: MSC4190 bot-device creation and `self_sign` against this
  Synapse, with a real registration.

## Resulting bridge config (deltas from `-e` output)

```yaml
homeserver:
  address: https://matrix.tjena.eu
  domain: tjena.eu
  websocket_proxy: http://127.0.0.1:<relay port>   # local relay → wss://wap.tjena.eu
  ping_interval_seconds: 30
appservice:
  id: wa-<localpart>
  bot: { username: wa_<localpart>bot }
  username_template: wa_<localpart>_{{.}}
  as_token / hs_token: from pairing
database: { type: sqlite3-fk-wal, uri: "file:<filesDir>/bridge.db?_txlock=immediate" }
provisioning: { shared_secret: <random 32+ chars> }
encryption:
  allow: true
  default: true
  require: true
  appservice: false
  msc4190: true
  self_sign: true
  delete_keys: everything true except delete_outbound_on_ack
  verification_levels: { receive: cross-signed-tofu, send: cross-signed-tofu, share: cross-signed-tofu }
bridge:
  permissions: { "<owner mxid>": admin }
  relay: { enabled: false }
```

## Server-side notes for M1

- `wap.tjena.eu` goes through the existing Cloudflare tunnel
  (`cloudflare_docker-cloudflared-1`, configured from the dashboard). Add a
  public hostname `wap.tjena.eu` → `http://tjena-wsproxy:29331` on
  `docker_local_net`, path `^/_matrix/client/unstable/fi\.mau\.as_sync$`
  (later also `^/provision/`). This has to be done in the Cloudflare
  dashboard.
- `tjena-wsproxy` joins `docker_local_net` without published ports.
  Registrations use `url: http://tjena-wsproxy:29331`.
- A registration's `url` is used for every Synapse→bridge call, so the
  proxy's `/_matrix/app/*` handling covers all of them.

## Next steps

1. Run the spike APK on the test phone (item 3). This is the last open M0 item.
2. M1: deploy `tjena-wsproxy`, admin CLI for registration and proxy entry, a
   bridge app that only starts the bridge from a pasted config plus the local
   relay, and a Synapse DB check that portal events are `m.room.encrypted`
   only.
