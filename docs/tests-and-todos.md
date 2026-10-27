# Personal bridge: what to test, what's left

State on 2026-10-22, on `main` (2026-10-22 changes not committed yet).

## Already verified automatically

| What | How |
|---|---|
| Gateway: slots, claim/reclaim/revoke, token swap, rate limit, audit log, state persistence | `go test -race ./...` (tjena_proxy repo) |
| Bridge config generation, no wildcard/domain permissions | `cd relay && go test ./...` |
| Bridge options (backfill, status updates) → bridge config; security-info (pickled account, owner trust, WhatsApp signature check); key-upload recorder | `cd relay && go test ./...` |
| Full stack against a throwaway Synapse 1.153: unmodified bridge → relay → gateway, slot activation, MSC4190 + self-sign, encrypted round trip, **only `m.room.encrypted` in Synapse DB**, pairing-code login API, isolation, moving to a new phone (fresh DB), revoke | `itest/run.sh` (23 checks, incl. "Security screen keys match Synapse") |
| tjena: list-logins parser · bridge app: QR link parser | `flutter test test/personal_bridge_test.dart` (tjena_chat repo) · `cd bridge_app && flutter test` |

## Your tests (in order)

**Server (production; needs your go-ahead, one Synapse restart)**

1. Build and start the gateway, create 20 slots: [tjena_proxy docs/server.md](https://github.com/tjena-eu/tjena_proxy/blob/main/docs/server.md) §1–4, 7.
2. `homeserver.yaml`: slot registrations, the two `experimental_features`
   flags, `login_via_existing_session`. Restart Synapse once.
3. **Check that the shared mautrix-whatsapp and mautrix-signal bridges still
   work** after the restart, because the experimental flags change their
   transactions too.
4. Cloudflare: public hostname `wap.tjena.eu` → `http://tjena-bridge-gateway:29331`.
   `curl https://wap.tjena.eu/provision/v1/info` should return
   `slots_available: true`.

**Phones**

5. Install `.build/tjena-bridge.apk` on the home phone. The Status tab should
   show the bridge version. This also closes M0 item 3: running the binary
   from an app on a real phone.
6. Install `.build/tjena-release.apk` (build 3714) on your phone. Go to
   Settings → Bridges → *WhatsApp: which bridge?* → Set up, then scan the QR
   with the bridge app. It should show *Online*.
7. Bridge app: Status → Battery *Fix*. WhatsApp tab → pairing code → link in
   WhatsApp.
8. tjena: *Check WhatsApp* → *Enable double puppeting*. The `login-matrix …`
   message in the bridge chat should be redacted. Then switch to **My own
   bridge**: the server bridge should be logged out and the old chats
   offered for archiving.
9. Text, image, voice note, reaction, edit and delete, in both directions.
   Then run the SQL check in tjena_proxy `docs/server.md` (portal rooms:
   encrypted only).
10. Reboot the home phone. The bridge should come back without you opening
    the app.
11. Take the home phone offline for 35 minutes. tjena should notify you;
    messages sent meanwhile should arrive when it's back.
12. Backup: export, then restore on another phone or after a reinstall. The
    old phone should stop with "another phone took over".
13. Remove the bridge in tjena. `slots-list` should show the slot `burned`,
    and the bridge app should show "stopped".
14. Leave it running for 48 hours (M2 acceptance criterion).

**Added 2026-10-22**

15. tjena: Settings → Messaging Bridges shows three WhatsApp options (server,
    your own, built-in) with where messages are decrypted. After updating, a
    phone that had the built-in bridge linked keeps it; otherwise it is off.
    Turn it on/off under *Experimental*; the built-in background service
    has its own switch (no notification when off).
16. Bridge app → Settings → Chats & history: pick *Lots*, save, then link a
    test WhatsApp account and check old messages arrive. Status updates *Off*:
    no "WhatsApp Status" chat appears.
17. Bridge app → WhatsApp → *Add another WhatsApp account*: both show on the
    WhatsApp tab and Status tab; tjena "Check WhatsApp" lists both.
18. Bridge app → Settings → Notifications → Minimal: the status-bar icon
    disappears after the restart. Stop Wi-Fi for 10 min: an alert appears.
19. Security: compare the fingerprints in both apps, then *verify bridge* in
    tjena; the bridge then shows as verified in tjena.

20. Removing a WhatsApp account cleans up: (a) built-in bridge → Remove
    account (box ticked): its chats and space vanish from tjena; Unlink offers
    the same box. (b) Bridge app → WhatsApp → log out: its chats and space
    vanish from tjena (they don't stay in the archive either). With *Remove
    its chats* off in Chats & history, they stay.

21. History import: link an account, open Status → History import. Phase 1
    fills while the phone sends history; phase 2 (chats created and filled)
    starts 5 min after the last piece. Afterwards "Not copied" should be 0
    and no chat should show a warning. If chats stopped with "rate limit",
    lift Synapse's message rate limit for your account and link again.

22. New chat → "WhatsApp chat" (your own bridge): your bridged chats are
    listed and filter as you type. Search a contact name or number → tap a
    result → the chat opens (an existing chat opens directly). Typing a full
    number offers "Start chat with +…". The built-in bridge entries only show
    when the built-in bridge is turned on.
23. History catch-up (bridge app 0.2.4): on an account with "chats stopped
    early", open History import → "Copy missing messages". The bridge
    restarts; the "Sending slowly" card shows waiting sends; afterwards "not
    copied" drops to 0 (chats with a newer live message keep their gap). The
    recovered messages appear at the end of each chat in the right order.
24. Fast history import (bridge app 0.2.8, double puppeting enabled
    before): WhatsApp tab → "Fast history import" on → link. The bridge
    restarts once before the code/QR appears. History import shows "Fast
    import: double puppeting paused"; your own old messages arrive as "you on
    WhatsApp" with no rate-limit waits. After the import (rooms created), the
    bridge restarts once more by itself and the card disappears; a new
    message sent from WhatsApp shows as you. Also check: cancelling the link
    turns double puppeting back on immediately.
25. WhatsApp calls (bridge app 0.3.0, tjena 2.7.0, **gateway redeployed**):
    a. Bridge app → Settings → Calls → turn on. Status shows "Not linked yet"
       and "Connected to the tjena server" within a few seconds. (If the
       server line stays on an error, the gateway is still the old version.)
    b. Link with "Get pairing code" (or QR). WhatsApp → Linked devices now
       lists "tjena calls" next to the bridge. Status: "Linked and online".
    c. tjena → a 1:1 WhatsApp chat of your own bridge → phone icon. Screen
       goes "Connecting to your bridge…" → "Calling on WhatsApp…" →
       "Ringing…"; the contact's phone rings. Answer: timer starts, audio both
       ways. Try mute, speaker, hang up from both sides.
    d. Have the contact call you. tjena gets the bridge's "Incoming call"
       message; with tjena open the incoming-call screen appears → Answer.
       From a notification: tap it, the chat opens and the call screen shows.
       Decline works too (the caller sees declined).
    e. Mobile data on the tjena phone (not home Wi-Fi): calls still connect
       (through the TURN server).
    f. Unlink in the bridge app → "tjena calls" disappears from WhatsApp.
26. WhatsApp calls with two accounts (bridge app 0.3.1, tjena 2.7.1):
    a. WhatsApp tab: every account card has a "Calls from tjena" row. Set up
       → pairing code for that account's number → enter it on that phone.
       A code entered on the wrong phone is refused with a message.
    b. Call from a chat of account A: the contact sees A's number calling.
       Same for B. The call screen shows "your account …1234".
    c. Call account B from another phone: the incoming screen says "to your
       account …(B)" and opens B's chat with that person, not A's.
    d. Log out account B in the WhatsApp tab → its calls device goes too;
       A keeps working.

27. WhatsApp video calls (bridge app 0.3.3, tjena 2.10.0; no gateway change):
    (a) In a WhatsApp chat tap the call button → "Video call". The WhatsApp
    contact gets a video call; once accepted, both see each other. Check the
    picture is upright on both sides (if tjena shows it sideways, note which
    way). (b) Mute the camera and turn it back on (both sides). (c) Voice call,
    then tap "Camera off" → it asks the contact to switch to video; accept on
    WhatsApp. Also decline once: tjena's camera turns off again. (d) Contact
    starts a voice call, then switches to video on WhatsApp → tjena shows
    "… wants to switch to video" → Switch. (e) Incoming video call → "Video"
    and once "Audio only". (f) Flip camera; drag the small preview. If video
    stays black, send the bridge app logs (calls section: "app video track",
    "peer video state").

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
- Nothing has run on a real phone yet: binary exec, Keystore,
  foreground service, boot start, Doze, Wi-Fi lock. Items 5, 10 and 14
  above cover this.
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
- Turning off the built-in bridge keeps its old chats in tjena; they just stop
  updating. There's no one-tap "migrate these chats" yet.
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
- 1:1 voice calls only. Video (H.264 could be passed through without
  transcoding) and group calls (meowcaller supports them) are not wired up.
  An incoming video call is answered with audio only.
- Incoming calls ring only while tjena is open or when you open the chat from
  the "Incoming call" notification within about 45 s. There is no
  full-screen ringing from the background yet (would need a dedicated push
  rule or a VoIP push).
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
  The bridge app has no "open-source licences" screen yet; add one before
  distributing the APK outside the family.

**By design (documented)**
- A revoked slot's real token stays valid inside Synapse until the next
  restart. It never leaves the server, and the slot is never reused.
- Burned slots accumulate, so add slots in batches (`slots-add`, one
  restart).
- Other Matrix clients (Element) may still show unverified-device warnings on
  bridged messages.

**Housekeeping**
- `spike-exec-app` was the M0 exec spike; the bridge app
  replaces it. It can be deleted.
- Nothing is committed yet.
