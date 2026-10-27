# tjena Bridge

Your own WhatsApp bridge for [tjena!chat](https://github.com/tjena-eu/tjena_chat):
an **unmodified** [mautrix-whatsapp](https://github.com/mautrix/whatsapp) running in
the tjena Bridge app on a spare Android phone at home. WhatsApp messages are
decrypted only on that phone and on your tjena devices; the server only passes
encrypted data along.

The server side (bridge gateway at wap.tjena.eu) is in [tjena_proxy](https://github.com/tjena-eu/tjena_proxy).

## Install

- **Obtainium (automatic updates):** add `https://github.com/tjena-eu/tjena_bridge`
  in [Obtainium](https://github.com/ImranR98/Obtainium), or open
  `obtainium://add/https://github.com/tjena-eu/tjena_bridge` on the phone.
- **APK:** [latest release](https://github.com/tjena-eu/tjena_bridge/releases/latest).
- Setup: [docs/user-guide.md](docs/user-guide.md) · website: <https://tjena.eu/download/>

Signing certificate SHA-256 (same key as tjena!chat):
`A9:4B:EA:04:5D:79:00:38:94:8B:9C:57:B3:39:AE:E5:79:D9:9B:0E:0C:08:FF:44:06:DC:EB:BC:A2:1F:44:46`

## Layout

| Path | What it is |
|---|---|
| `bridge_app/` | The tjena Bridge Android app (Flutter + Kotlin service) |
| `relay/` | Runs on the phone next to the bridge: forwards to the gateway, config generation, diagnostics (`history-info`, `names-info`, …) |
| `wacall/` | WhatsApp calls daemon (one calls device per WhatsApp account) |
| `android-binary/` | Builds mautrix-whatsapp (pinned tag, unmodified), relay and wacall for android/arm64 into the app's `jniLibs` |
| `itest/` | Full-stack test against a throwaway Synapse (`itest/run.sh`; needs tjena_proxy checked out next to this repo, or `GATEWAY_DIR`) |
| `docs/` | User guide, design notes, tests and to-dos |
| `spike-exec-app/` | Early feasibility spike (exec of native binaries on Android) |

## Build

```bash
bash android-binary/build.sh                     # native binaries (Go + Android NDK)
cd bridge_app && flutter build apk --release --target-platform android-arm64
```

Release signing reads `bridge_app/android/key.properties` (git-ignored) or the file
named by `TJENA_KEY_PROPERTIES`. Use the same key for every release, or installed
apps can't update.

Tests: `cd relay && go test ./...`,
`cd bridge_app && flutter test`, `itest/run.sh` — see [docs/tests-and-todos.md](docs/tests-and-todos.md).

## Release

GitHub releases (what Obtainium installs) are made with fastlane:
[bridge_app/android/fastlane/README.md](bridge_app/android/fastlane/README.md).

## License

AGPL-3.0-or-later, see [LICENSE](LICENSE). mautrix-whatsapp is AGPL-3.0 as well and is
built from its upstream source without changes.
