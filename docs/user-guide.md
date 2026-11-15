# Your own WhatsApp bridge: user guide

With your own bridge, your WhatsApp messages are decrypted only on a phone
you own (at home) and on your tjena devices. The tjena server only passes
encrypted data along.

**You need:** a spare Android phone (Android 10 or newer, 64-bit) that stays
at home on Wi-Fi and a charger, plus your tjena app.

> WhatsApp doesn't officially allow third-party clients. The bridge connects
> as a "linked device", and WhatsApp may restrict or ban accounts that use
> unofficial clients. Use it at your own risk.

## Setup (about 10 minutes)

1. **Install tjena Bridge** on the home phone (`tjena-bridge.apk`). Allow
   notifications and the camera when asked.
2. **In tjena (your normal phone):** Settings → Bridges →
   *WhatsApp: which bridge?* → **Set up**. Enter your tjena password. tjena
   shows a QR code that is valid for 5 minutes and works once.
3. **In tjena Bridge:** confirm the risk note, tap **Scan the QR code from
   tjena** and scan the code. The bridge app signs in as its own session
   ("WhatsApp bridge (home)"), gets your personal bridge from the server and
   starts it. Without a camera (or if tjena can't show a QR code) tap **Sign
   in with username and password** instead.
4. **Battery:** on the Status tab, tap **Fix** next to "Battery optimisation"
   and allow tjena Bridge to run unrestricted. Otherwise Android may stop it.
5. **Link WhatsApp:** tjena Bridge → WhatsApp tab → enter your number → **Get
   pairing code**. In WhatsApp on your phone: Settings → Linked devices →
   Link a device → *Link with phone number instead* → enter the code. (If
   WhatsApp runs on a different phone, use **Show QR code** and scan it with
   WhatsApp.)
6. **Back in tjena:** the bridge page shows *Online*. Tap **Check WhatsApp**,
   then **Enable double puppeting** (recommended). This makes messages you
   send from the WhatsApp app show up in tjena as sent by you. tjena creates a
   separate session "WhatsApp bridge double puppet (home)" for this. You can
   log it out any time under Settings → Devices.
7. **Switch:** at the top, choose **My own bridge**. tjena first logs your
   WhatsApp out of the shared tjena server bridge, so no chat appears twice.
   It then offers to archive the old WhatsApp chats; they stay readable under
   Archive.

## Everyday use

- Bridged chats show a small label above the messages, e.g.
  *WhatsApp · your own bridge (home)*. Tap it for details.
- If your bridge is offline for more than 30 minutes (adjustable under
  Advanced), tjena notifies you. Messages you send while it's offline are
  delivered when it comes back.
- The home phone shows a permanent "tjena Bridge" notification while the
  bridge runs. It restarts automatically after a reboot.

## tjena Bridge app screens

| Tab | What it does |
|---|---|
| **Status** | Bridge process, connection to tjena, WhatsApp state of every account, encryption (→ View keys), last event, battery state, version, recent log |
| **WhatsApp** | Link WhatsApp (pairing code or QR), **add more WhatsApp accounts**, log out |
| **Access** | You are always the admin. You can let other tjena users use your bridge, but their WhatsApp messages would then be decrypted on your phone. Not recommended. Wildcards and whole servers are not allowed. |
| **Settings** | *Chats & history* (old messages, WhatsApp status updates), *Security*, notifications, bridge proxy (advanced), restart/stop, *Make this phone the active bridge*, export log (without secrets, numbers or message text), unpair |
| **Backup** | Passphrase-encrypted backup of the bridge (WhatsApp session and encryption keys) and restore |

## Chats & history

Bridge app → Settings → **Chats & history**:

- **Message history:** *Off*, *Light* (20 messages in your 10 latest chats),
  *Standard* (50 messages in your 25 latest chats, the default), *Lots* (500
  messages in every chat plus a year of history) or *Custom*. WhatsApp sends
  the history once, right after linking, so the number of chats and "a year of
  history" only apply the next time you link.
- **WhatsApp status updates:** *Off* (default), a *muted* chat, or a normal
  chat. If a "WhatsApp Status" chat already exists, leave it in tjena.

Saving restarts the bridge.

## More than one WhatsApp account

WhatsApp tab → **Add another WhatsApp account**, then link it like the first
one. Each account gets its own chats in tjena.

## Notifications

Android needs a notification while the bridge runs. Bridge app → Settings →
Notifications → **Minimal** hides it from the status bar. **Alert me about
problems** shows a separate notification when the bridge stops or can't reach
tjena for 10 minutes. *Android notification settings* lets you silence or hide
them completely.

## Security: checking the keys

Both apps show the keys, grouped in blocks of four:

- tjena: Settings → Messaging Bridges → Your own bridge → **Security**
- tjena Bridge: Settings → **Security** (or Status → *View keys*)

Compare **Bridge identity** and **Bridge device**, and check that **Your
identity** in the bridge app matches *Your identity* in tjena. If they match,
tap *Fingerprints match: verify bridge* in tjena. The bridge app also shows
which of your devices the bridge trusts, and on the WhatsApp side the linked
device key, your WhatsApp account key, and whether your phone's approval of
the bridge checks out.

## Moving to a new phone

- **Move (recommended):** on the old phone, Settings → **Move to a new
  phone**. On the new phone, install tjena Bridge, sign in with the same tjena
  account and choose **Move from my old phone**. Then either scan the old
  phone's code, or tap **Connect without a code** (no camera needed): both
  phones show a 6-digit number; if it is the same, confirm on both. Chats,
  the WhatsApp link and the keys move over end-to-end encrypted, and the old
  phone removes its copy. Works the same with a Docker bridge.
- **With a backup:** on the old phone, Backup → Export. On the new phone,
  install tjena Bridge → **Restore from a backup file**. The old phone stops
  automatically when the new one connects.
- **Without a backup:** set up again from tjena (Set up → scan) on the new
  phone and link WhatsApp again. The old phone is cut off. Older bridged
  messages may then show as "unable to decrypt" on newly added tjena devices.

## Removing the bridge

In tjena: WhatsApp bridge → **Remove my bridge**, or in tjena Bridge:
Settings → **Unpair and remove bridge**. Either way, WhatsApp is logged out,
the server stops accepting the bridge immediately, and the bridge app's
session is signed out. Your chats stay readable.

## Good to know

- **Verified devices:** the bridge only accepts messages from your
  cross-signed tjena devices, and only shares WhatsApp messages with them. If
  a device is not verified, verify it under Settings → Devices.
- **Calls:** one-to-one WhatsApp voice and video calls work from tjena
  (beta). Set them up per account: bridge app → WhatsApp tab → *Calls from
  tjena* → **Set up**.
- **Other Matrix apps** (e.g. Element) may still show "unverified device"
  warnings on bridged messages.
- **Privacy:** the server sees that you use a bridge, when it is online and
  how much traffic it has, but not your messages.
