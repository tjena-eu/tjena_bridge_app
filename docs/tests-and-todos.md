# Personal bridge: what to test, what's left

State on 2026-11-11, on `main` (changes since 2026-10-22 not committed yet).

## Still to test

Already done and removed from this list: server setup and gateway, installing
and linking the bridge app, switching to your own bridge, double puppeting,
everyday messaging, two WhatsApp accounts, outgoing voice calls, running for
days. The built-in bridge tests are gone too (removed in tjena 2.8.0).

### Incoming WhatsApp calls (tjena 2.11.5, build 3737)

Call your WhatsApp number from another phone each time. If a call drops,
connect the phone by USB during the call (`adb logcat flutter:I`) and note
what the screen said last.

- [ ] tjena **in the background**: the system call screen rings → Accept.
      tjena's call screen opens, goes "Answering…" → "Connecting…" → timer,
      audio both ways, and **stays open** (it used to close right after
      "Connecting…", like a hang-up).
- [ ] tjena **closed** (swiped away): same as above.
- [ ] Phone **locked**: Accept on the lock screen. tjena's call screen opens
      **over the lock screen without unlocking**, and you accept **once**
      only (no second ringing screen). Hang up: the phone is back on the
      lock screen, still locked. Try once with tjena swiped away before
      locking, and once with Accept in the notification instead of the
      full-screen call screen.
- [ ] A Matrix (Element Call) call on the lock screen still asks to unlock
      after Accept, as before.
- [ ] Speaker button during a voice call: the call gets loud (loudspeaker);
      tap again: back to the earpiece. With Bluetooth headphones the call
      stays on them. A video call starts on the loudspeaker.
- [ ] During a call, tap the running-call notification: it brings you back to
      the call screen.
- [ ] Decline on the system call screen: the caller sees declined, and tjena
      shows no ringing screen afterwards.
- [ ] While it rings, open tjena from the launcher (don't tap Accept): the
      call must **not** be answered by itself.
- [ ] tjena **open** in the foreground: tjena's own incoming-call screen
      appears → Answer → call screen stays open. Decline there works too.
- [ ] The chat, the chat list preview and the notification say "Incoming
      WhatsApp call" (or "… video call"), not "Use the WhatsApp app to
      answer". Old call notices change too. A chat of the **server** bridge
      keeps the bridge's original text.
- [ ] Main WhatsApp phone switched off: an incoming call still works. Only the
      home bridge phone has to be online.

### Other WhatsApp call checks

- [ ] Mobile data on the tjena phone (not home Wi-Fi): calls still connect
      (through the TURN server).
- [ ] Unlink "Calls" in the bridge app → "tjena calls" disappears from
      WhatsApp → Linked devices.
- [ ] Two accounts: every account card in the bridge app's WhatsApp tab has a
      "Calls from tjena" row. A pairing code entered on the wrong phone is
      refused with a message.
- [ ] Two accounts: calling from a chat of account A, the contact sees A's
      number; same for B. The call screen shows "your account …1234".
- [ ] Two accounts: someone calls account B → the incoming screen says "to
      your account …(B)" and opens B's chat with that person, not A's.
- [ ] Two accounts: log out account B in the WhatsApp tab → its calls device
      goes too; A keeps working.

### WhatsApp video calls (bridge app 0.3.3, tjena 2.10.0)

If video stays black, send the bridge app logs (calls section: "app video
track", "peer video state").

- [ ] Call button → "Video call": once accepted, both see each other, picture
      upright on both sides (it was upside down in tjena). Try with an
      Android and, if possible, an iPhone on the WhatsApp side, and turn the
      WhatsApp phone sideways once. If wrong, note which way and the bridge
      log line "peer video state … orientation".
- [ ] Camera off and back on, on both sides.
- [ ] Voice call → "Camera off" button asks the contact to switch to video;
      accept on WhatsApp. Decline once too: tjena's camera turns off again.
- [ ] Contact switches a voice call to video on WhatsApp → tjena shows "…
      wants to switch to video" → Switch.
- [ ] Incoming video call → answer with "Video", and once with "Audio only".
- [ ] Flip camera; drag the small preview.

### Home bridge phone

- [ ] Reboot the home phone. The bridge comes back without opening the app.
- [x] Take the home phone offline for 35 minutes. tjena notifies you; messages
      sent meanwhile arrive when it's back.
- [ ] Bridge app → Settings → Notifications → Minimal: the status-bar icon
      disappears after the restart. Stop Wi-Fi for 10 min: an alert appears.
- [x] Bridge app → Settings → Chats & history → Status updates *Off*: no
      "WhatsApp Status" chat appears.
- [ ] Backup: export, then restore on another phone or after a reinstall. The
      old phone stops with "another phone took over".
- [ ] Remove the bridge in tjena. `slots-list` shows the slot `burned`, and
      the bridge app shows "stopped".

### Messages and chats

- [ ] Voice note, reaction, edit and delete, in both directions.
- [ ] New chat → "WhatsApp chat": your bridged chats are listed and filter as
      you type. Search a contact name or number → tap a result → the chat
      opens (an existing chat opens directly). Typing a full number offers
      "Start chat with +…".
- [x] Bridge app → WhatsApp → log out an account: its chats and space vanish
      from tjena (not into the archive either). With *Remove its chats* off
      in Chats & history, they stay.

### History import

- [x] Link an account, open Status → History import. Phase 1 fills while the
      phone sends history; phase 2 (chats created and filled) starts 5 min
      after the last piece. Afterwards "Not copied" is 0 and no chat shows a
      warning.
- [x] On an account with "chats stopped early": History import → "Copy
      missing messages". The bridge restarts; the "Sending slowly" card shows
      waiting sends; afterwards "not copied" drops to 0 (chats with a newer
      live message keep their gap). Recovered messages appear at the end of
      each chat in the right order.
- [x] Fast history import (double puppeting on): WhatsApp tab → "Fast history
      import" on → link. The bridge restarts once before the code/QR appears.
      History import shows "Fast import: double puppeting paused"; your own
      old messages arrive as "you on WhatsApp" with no rate-limit waits.
      Afterwards the bridge restarts once more by itself and the card goes; a
      new message sent from WhatsApp shows as you. Cancelling the link turns
      double puppeting back on immediately.

- [ ] History import screen (bridge app 0.3.6 build of 2026-11-06): chats
      that can still be completed show an orange repair symbol and are
      counted under "Copy missing messages". Chats that can't be completed
      (newer messages already in the chat) show a grey "blocked" symbol, sit
      in their own grey card, and are **not** offered for copying.
- [ ] A chat the bridge fails to create twice (see "Failed to get chat info"
      under Errors) shows "can't be created" with the grey symbol. After the
      next bridge restart, held-back chats behind it get created again, and
      the screen no longer says "the bridge has not finished creating chats".

### Moving the bridge to a new phone (bridge app 0.4.0, gateway with move.go)

Needs the new gateway deployed first.

- [ ] Old phone: Settings → Move to a new phone shows a QR code; the bridge
      keeps running while it waits. New phone: install tjena Bridge, sign in
      with the same account → "You already have a bridge" → Move from my
      old phone → scan. Progress shows on both; the old phone says "Moved"
      and is back on the setup screen.
- [ ] Same move with **Connect without a code** (e.g. on a phone without a
      camera, which shows the password sign-in right away): both phones show
      the same 6-digit number; the transfer starts only after confirming on
      both. Tap "Different number" on either phone: nothing is sent, the
      bridge keeps running on the old phone.
- [ ] Sign-in screen: "Scan the QR code from tjena" and "Sign in with
      username and password" are both full buttons; "Restore from a backup
      file" is visible without opening Advanced.
- [ ] Afterwards on the new phone: same chats in tjena (no duplicates),
      WhatsApp still linked (no new code), messages both ways, calls work,
      Security screen shows the same bridge keys. WhatsApp → Linked devices
      shows no new device.
- [ ] tjena → Settings → devices: the old phone's "WhatsApp bridge (home)"
      session is gone, the new phone's is there.
- [ ] Leave the old phone's QR screen without scanning: the bridge keeps
      running there. Scan an old code (screen reopened since): the new phone
      says the old phone isn't waiting for this code.
- [ ] Turn off Wi-Fi on the new phone while it receives: the old phone says
      the transfer was interrupted and its bridge runs again.
- [ ] Old phone lost: on the new phone choose "Start fresh (old phone lost)":
      link WhatsApp again; the old phone (if it comes back) shows another
      phone took over.

### Docker bridge (docker/, web UI)

Needs the new gateway deployed for the move checks.

- [ ] First run of the GitHub workflow (push a `v*` tag or run it by hand):
      both architectures build, `ghcr.io/tjena-eu/tjena-bridge` shows
      amd64 + arm64. Set the package to **public** once (GitHub → org →
      Packages → tjena-bridge → Package settings), then `docker compose pull`
      works without a login.
- [ ] Image on a Raspberry Pi (arm64): starts, links WhatsApp.
- [ ] `docker compose up -d` on a home server with only `compose.yaml`, open
      `http://<server-ip>:8080` from a laptop in the same network, choose a
      password. Sign in with a test tjena
      account, link WhatsApp with a pairing code and once with the QR code.
      Messages both ways; the Status tab shows "Connected to tjena".
- [ ] Phone → Docker without a code: phone Settings → Move to a new phone;
      web UI "You already have a bridge" → Move from my old device →
      Connect without a code. Same number on both; confirm on both. Same
      chats, no new WhatsApp code, the phone is back on its setup screen.
- [ ] Phone → Docker with the pasted code (Copy code on the phone).
- [ ] Docker → phone: Backup & move → Move to another device, scan the QR
      code with the phone during its setup.
- [ ] Backup made in the web UI restores on the phone, and the other way.
- [ ] Calls on a Docker bridge (default network): audio both ways, video.
- [ ] `docker restart tjena-bridge`: the bridge comes back by itself; the web
      UI asks for the password again.

### Security and server

- [ ] Compare the fingerprints in both apps, then *verify bridge* in tjena; the
      bridge then shows as verified.
- [ ] Run the SQL check in tjena_proxy `docs/server.md`: portal rooms hold
      encrypted events only.
- [ ] The shared mautrix-whatsapp and mautrix-signal bridges on the server
      still work (the experimental Synapse flags change their transactions
      too).

### tjena app

- [ ] Open bug: on mobile data with Data Saver **off** and Background data
      **on**, the Pixel shows "With Data Saver on" under Settings → Chats →
      Auto-download media. Check whether Battery Saver is on at the time, and
      with the phone on USB run
      `adb shell "dumpsys netpolicy | grep -iE 'restrict background|restrict power|low_power'"`.

## Automated tests (rerun after changes)

| What | How |
|---|---|
| Gateway: slots, claim/reclaim/revoke, token swap, rate limit, audit log, state persistence | `go test -race ./...` (tjena_proxy repo) |
| Bridge config generation, no wildcard/domain permissions | `cd relay && go test ./...` |
| Bridge options (backfill, status updates) → bridge config; security-info (pickled account, owner trust, WhatsApp signature check); key-upload recorder | `cd relay && go test ./...` |
| Full stack against a throwaway Synapse 1.153: unmodified bridge → relay → gateway, slot activation, MSC4190 + self-sign, encrypted round trip, **only `m.room.encrypted` in Synapse DB**, pairing-code login API, isolation, moving to a new phone (fresh DB), revoke | `itest/run.sh` (23 checks, incl. "Security screen keys match Synapse") |
| Move key exchange: phone (Kotlin) and Docker (Go) give the same key, number and proofs for fixed keys | `cd bridge_app/android && ./gradlew :app:testDebugUnitTest` · `TestKexVectors` in docker/manager |
| Docker manager: backup/move formats, move end to end through a fake gateway (with code, comparing numbers, numbers differ on either side, wrong code), web UI auth, CSRF guard, home-network check and first-password host check, log redaction | `cd docker/manager && go test -race ./...` (also runs in the image build) |
| tjena: list-logins parser · bridge app: QR link parser | `flutter test test/personal_bridge_test.dart` (tjena_chat repo) · `cd bridge_app && flutter test` |

## Todos and known gaps

- Contact search and starting chats go through the bridge bot's `search` and
  `start-chat` commands, so each search leaves a command and a reply in the
  chat with your bridge. A direct provisioning-API path via the gateway would
  avoid that.

- History import loses the rest of a chat when one message fails to send
  (mautrix-go `sendLegacyBackfill` stops at the first error). The likely
  trigger is Synapse's per-user message rate limit: your own messages are
  sent through double puppeting with your normal account, and about 25 chats
  fill in parallel. Fix on the server: `override_ratelimit` for the owner
  (admin API) or a higher `rc_message`. The itest sets `rc_message` to 1000,
  so it doesn't catch this. Confirmed on a real account (18 chats, 730
  messages). Without server changes (bridge app 0.2.4): the relay paces sends
  per sender and retries 429s itself (relay/pace.go), and "Copy missing
  messages" re-runs the bridge's own portal creation for chats with leftover
  stored history (relay `history-catchup`), which forward-fills them. Gaps
  before a newer live message can't be filled.

**Must check before relying on it**
- **The shared server bridge's encryption is probably not working today**
  (not changed by this work). Synapse refuses `/sync` for appservice users, and
  the experimental flags were never enabled. I couldn't read its
  `config.yaml` (permission denied). After the restart with the flags, it
  could switch to `encryption.appservice: true`.
- The bridge only accepts messages from **cross-signed** tjena devices
  (`cross-signed-tofu`, as the brief asks). Messages from a non-verified
  device are dropped by the bridge. Consider an in-app hint, or relax `send`
  to `unverified`.

**Missing features**
- Per-contact WhatsApp security codes aren't shown (WhatsApp's exact
  algorithm for linked devices isn't documented; a wrong number would look
  like an attack). Compare in WhatsApp on your phone instead.
- No `ffmpeg` in the bridge app: GIF→video and some voice/video conversions
  are off. Bundle an arm64 `ffmpeg` as `libffmpeg.so` and put it on `PATH`.
- Animated stickers (`lottieconverter`) aren't available either.
- Switching bridges relies on parsing the bot's `list-logins` text (format
  checked against mautrix-go v0.32). "Archive" means leaving the old portal
  rooms.
- No in-app update channel for the bridge app. New bridge versions ship as a
  new APK (`PINNED_TAG` in `android-binary/build.sh`).
- The gateway is a single instance with a JSON state file. That's fine at
  tjena's size.
- tjena web and iOS: the own-bridge page uses only HTTP and OpenID, so it
  should work there, but it's untested. The offline notification is
  Android/iOS only.

**WhatsApp calls**
- 1:1 voice and video calls (video since tjena 2.10.0). Group calls
  (meowcaller supports them) are not wired up.
- Incoming calls ring on the system call screen (also locked or with tjena
  closed) through the normal push for the bridge's "Incoming call" message,
  so they depend on that push arriving quickly. No VoIP push.
- The main WhatsApp phone doesn't need to be online for calls. WhatsApp
  logs out all linked devices (bridge and calls device) if the main phone
  isn't online for about 14 days.
- The "Incoming call" text in the chat comes from the unmodified bridge; tjena
  only replaces it when showing it.
- Media auto-download: Android can't tell tjena whether Data Saver or a
  per-app restriction limits it, so the label says "Data Saver" for both.
- Audio on the WebRTC leg is G.722 (16 kHz, same as WhatsApp's), PCMU as a
  fallback. Opus would need a C library on the bridge phone.
- Each WhatsApp account gets its own calls device: a second linked device
  on that account (WhatsApp allows 4). Accounts are matched by phone number
  (bridge login ID = the portals' `fi.mau.receiver`).
  The bridge's own device can't be shared with it without modifying the
  bridge.
- meowcaller is unofficial and young; WhatsApp can change its call protocol at
  any time. Updating means bumping its version in `wacall/go.mod`.
- Licences of everything bundled into libwacall.so: `wacall/THIRD_PARTY.md`.
  The bridge app shows them under Settings → About & licences (AGPL-3.0,
  like tjena; texts in `bridge_app/assets/licenses/`, copied from `LICENSE`
  and this file, so update both when they change).

**By design (documented)**
- A revoked slot's real token stays valid inside Synapse until the next
  restart. It never leaves the server, and the slot is never reused.
- Burned slots accumulate, so add slots in batches (`slots-add`, one
  restart).
- Other Matrix clients (Element) may still show unverified-device warnings on
  bridged messages.

- Moving to a new phone keeps the whole bridge database in memory while it
  is encrypted (like the backup). Fine for normal databases (tens of MB);
  a very large one could run out of memory on a small phone.
- The move goes through the gateway (encrypted with the key in the QR code,
  or one agreed by both devices and checked with the 6-digit number);
  there's no direct phone-to-phone path on the same Wi-Fi. If the server
  path fails, the backup file still works.

**Housekeeping**
- `spike-exec-app` was the M0 exec spike; the bridge app
  replaces it. It can be deleted.
- The latest changes in tjena_chat and here are not committed yet.
- tjena_chat was moved to `vm_projects/tjena/tjena_chat`. Build with Flutter
  3.47.7 (`FLUTTER=/mnt/SSD/PhD_local_only/tools/flutter-3.47.7/bin/flutter
  bash build_android`, now the default). `build_android` also patches
  flutter_callkit_incoming (patches/callkit_lockscreen_fix.py). `android/key.properties` now
  points at the keystore's new path.
