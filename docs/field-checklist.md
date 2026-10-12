# Field checklist (real hardware)

Run this once before a release, by hand, on real devices. It covers what automated tests cannot:
phones, Wi-Fi behaviour, OS trust stores, notifications and the real Safari.

**Equipment**

| Role | Device |
|------|--------|
| Server host A | Windows 11 PC (the "main" host) |
| Server host B | Raspberry Pi 4 (64-bit OS, wired or Wi-Fi) |
| Clients | iPhone (Safari), Android phone (Chrome), Mac (Safari and Chrome), the Windows 11 PC |
| Network | A normal home or office Wi-Fi/LAN, plus a guest SSID with client isolation for step 7 |
| Internet | Needed only for step 6 (Web Push) |

**How to use it.** Work top to bottom. Tick a box only when you saw the result with your own eyes.
Write the date, build version (`bubbles version`) and any failure in the notes line. A failure is a bug:
file it with the step number.

Build under test: ______________________  Date: ______________  Tester: ______________

Commands below use `bubbles` for the server binary. On Windows use `bubbles.exe`.

---

## 1. Install and start the server

### 1.1 Windows 11 host

- [ ] Unzip the release, run `bubbles.exe serve` from a terminal. The console prints the HTTPS and HTTP addresses, the CA
  fingerprint and the admin banner (`Sign in as: admin` with a generated password).
- [ ] Windows Firewall asks to allow `bubbles.exe`; allow Private networks only.
- [ ] Open the printed HTTPS address on the host itself: the certificate warning appears (the CA is not trusted yet).
- [ ] Sign in as `admin` with the password from the console. Restart the server before changing the password: the console
  says `The first admin password is in <path>`.
- [ ] Change the password to a strong one of your own. The server accepts any password up to 128 characters, so the choice is yours.
- [ ] After the change, `admin-password.txt` is gone from `%LOCALAPPDATA%\bubbles` (`dir %LOCALAPPDATA%\bubbles`).

### 1.2 Raspberry Pi 4 host (repeat steps 2 to 4 later against this host)

- [ ] Copy `bubbles-<version>-linux-arm64` to the Pi, `chmod +x`, run `./bubbles serve --data-dir ~/bubbles-data`.
- [ ] The first start finishes in under 10 seconds; the console shows addresses and the admin password banner.
- [ ] `./bubbles healthcheck --data-dir ~/bubbles-data` prints OK.
- [ ] `top` or `htop`: the server's resident memory is below 100 MB when idle.

Notes: ______________________________________________

## 2. Onboarding with QR and CA install (each client)

Open the onboarding address printed on the server console in a browser on the host (it shows the QR code), scan that QR
code with each device below (or type the address) and follow the on-screen steps. The page must work with no internet.

| Device | CA installed | Opens without a certificate warning | Account created (name and password) | Notes |
|--------|:-----------:|:-----------------------------------:|:---------------:|-------|
| iPhone (Safari: profile, then Certificate Trust Settings) | [ ] | [ ] | [ ] | |
| Android (Settings, Install a certificate, CA certificate) | [ ] | [ ] | [ ] | |
| Mac (`security add-trusted-cert` or Keychain Access) | [ ] | [ ] | [ ] | |
| Windows 11 (`Import-Certificate ... Cert:\CurrentUser\Root`) | [ ] | [ ] | [ ] | |

- [ ] On each device, compare the fingerprint shown on the onboarding page with the one on the server console: they match.
- [ ] On Android, if `bubbles.local` does not resolve, the numeric address listed on the page works.
- [ ] Phone registration: on a phone, open `http://<server>:28226` (or scan the QR code), trust the CA, tap **Create your account**
  (name and password); you land signed in.
- [ ] Invitation: in Admin, Invites, create a link; open it on a second phone; create the account with a name and password; you land signed in.

Notes: ______________________________________________

### 2.1 Sign-up window, closing and Add person

- [ ] On a fresh start, Admin, Settings, **Sign-up** shows "Open until …" about 24 hours ahead, and the switch "Anyone on the network can create an account" is on.
- [ ] Press **Close now**: the state shows "Closed". The onboarding page and `/register` say sign-up is closed and ask for an invite link.
- [ ] Restart the server: sign-up is still closed.
- [ ] Press **Reopen**: the state shows "Open until …" again and a new account can be created at `/register`.
- [ ] Optional, slow: set **Close sign-up automatically after** to 1 hour, wait for it to pass, and confirm it closes on its own.
- [ ] A name that already exists (in any letter case) is refused at sign-up with a clear message; the @handle is made from the name (for example "Ada Lovelace" gives @ada-lovelace).
- [ ] Admin, Users, **Add person** (name, password, role `member`): the new account signs in on a phone.

Notes: ______________________________________________

## 3. Chat across all devices

Sign in on all four clients with different accounts (invite two more members if needed). In one shared channel:

- [ ] A message sent from each device appears on all other devices within about a second, in order.
- [ ] Typing indicator shows on the others while you type.
- [ ] Reaction, edit, delete and a thread reply propagate to all devices.
- [ ] `@mention` of the phone user: the phone shows a mention badge; unread counts clear after reading.
- [ ] Put the iPhone to sleep for a minute, send three messages from the Mac, wake the iPhone: the messages appear without a reload
  and nothing is duplicated.
- [ ] Switch the Android phone to airplane mode, send two messages from the Mac, switch back: they arrive, in order.
- [ ] Dark and light themes both readable; the phone layout shows list, channel and thread as stacked screens with back buttons.

### 3.1 iPhone photo upload (GPS stripped)

- [ ] On the iPhone, take a photo with Location Services on for the Camera app and upload it from the composer (Photo Library or Take Photo).
- [ ] The photo shows in the channel with a thumbnail and the lightbox opens it.
- [ ] Download the image on the Mac and inspect it (`exiftool photo.jpg` or Preview, Tools, Show Inspector, GPS tab): **no GPS, no
  camera serial, no EXIF location**. The orientation is correct (upright).
- [ ] A HEIC photo (Settings, Camera, Formats, High Efficiency) is accepted and offered as a download; the file card says metadata was not removed.

### 3.2 netdiag-sender alert

- [ ] In Admin, Apps, create an app or approve a pairing request; give it `alerts:write`; copy the connection string.
- [ ] On the Windows host run `examples\netdiag-sender --conn "<connection string>" --demo`.
- [ ] A critical alert card appears in the alert channel on every client with severity shape and word, fields and an "x N" counter.
- [ ] Repeating the demo raises the counter instead of posting a new card; the "OK again" notice resolves it ("Resolved").
- [ ] Acknowledge the alert from the phone: the state chip reads "Acknowledged by <name>" on every device.

Notes: ______________________________________________

## 4. Tray app: join and notifications (Windows, macOS and Linux)

On the Windows 11 PC and on the Mac, install the tray app from the release.

- [ ] First run: the tray finds the server (a **Join <instance> · <address>** button) or takes a typed address, then shows a confirm step with the server name, the address and the CA fingerprint. The fingerprint equals the one on the console. Windows and macOS show their own CA install prompt; accept it.
- [ ] Create account from the tray (**Your name**, password, name of this computer): the chat window opens already signed in, with no second sign-in.
- [ ] Sign in with a normal account (not admin is fine). The tray icon turns to "connected".
- [ ] Wrong password: a generic error; five wrong attempts lock the account for a while (and the web sign-in shows the same lockout).
- [ ] Send a message mentioning the tray user from the iPhone: a system notification appears on the desktop. It shows the sender and
  channel, and the preview follows the notification privacy setting.
- [ ] Click the notification: the browser or tray window opens directly in that channel without a second sign-in.
- [ ] Mute the channel: no notification for new messages. Do-not-disturb on the OS: no sound.
- [ ] Sign out in the tray: the device is revoked on the server (`GET /api/v1/me/devices` no longer lists it), and the tray returns to sign-in.
- [ ] Stop the server for 30 seconds and start it again: the tray reconnects by itself and shows missed alerts.
- [ ] Linux (any Linux computer with the tray app): the tray shows the steps to trust the CA instead of installing it; after you
  follow them, the join continues and the account is created or signed in.
- [ ] Optional: a fake or unconstrained CA is refused. Offer the tray a test CA without critical name constraints limited to local
  names and private IP ranges: the tray shows an error and installs nothing.

Notes: ______________________________________________

## 5. Alerts and notification rules

- [ ] Critical alert with the phone locked and the browser closed: see step 6 (Web Push). On the desktop tray the notification is shown.
- [ ] Per-channel notification level "mentions only" suppresses ordinary messages but not critical alerts at or above the channel's minimum severity.

Notes: ______________________________________________

## 6. Web Push on Android (needs internet)

- [ ] In Admin, Settings, enable Web Push. The server needs outbound internet to the push service; the console shows no errors.
- [ ] On Android (Chrome, installed to the home screen or open in Chrome), Settings, Notifications, turn on Web Push and allow notifications.
- [ ] Lock the phone and close Chrome. Send a message mentioning the user from another device: a notification arrives within a few seconds,
  showing only the generic text when previews are off.
- [ ] Tap it: the app opens in the right channel.
- [ ] Turn Web Push off in Settings, or revoke it in Android settings: no more notifications; the server shows the subscription gone.
- [ ] Disconnect the server host from the internet: chat on the LAN keeps working; push attempts fail quietly (check the console shows a short warning, no message text).

Notes: ______________________________________________

## 7. Client isolation, mDNS and discovery

- [ ] Join a phone to a guest SSID that isolates clients, then open the server address: the page does not load. The server console
  warns after about 10 minutes that no other device has connected (AP isolation hint), and Admin, Overview shows the same hint.
  The onboarding page and docs explain what to do.
- [ ] Switch the phone to the normal SSID: it connects.
- [ ] mDNS: from the Mac, `ping bubbles.local` (or the chosen `--mdns-name`) answers with the host's address; `dns-sd -B _bubbles._tcp` (Mac)
  lists the server.
- [ ] From the iPhone and the Mac, `https://bubbles.local:28225` opens without warnings. On Android check whether `.local` resolves;
  if not, the numeric address works.
- [ ] Start two servers with different `--mdns-name` values on one LAN: both are discoverable and neither takes the other's name.

Notes: ______________________________________________

## 8. End of LAN party drill

Goal: wipe content, wipe everything, and prove the phones reconnect without reinstalling the CA.

1. [ ] With all clients signed in and the channel full of messages, photos and an alert: Admin, Danger zone, **Clear content**.
   Type the instance name and the admin password. All devices drop their cached messages and reconnect by themselves (close code 4011);
   accounts and channels remain; no old message is visible on any device after a reload.
2. [ ] Admin, Danger zone, **Factory reset** with *Generate new CA* **off** and *Keep server settings* **off** (CA kept). Optionally
   download the encrypted backup first. Confirm with the instance name and password.
3. [ ] All clients show "This server was reset" (close code 4010) and return to the start page. Cached data and drafts are gone
   (check in the browser: Application, Storage shows no bubbles IndexedDB or localStorage content).
4. [ ] The console prints the new admin password, and the reset-complete screen shows it once. Sign in as `admin` with it, change it, then create the new accounts.
5. [ ] **The iPhone, Android phone and Mac, which already trust the CA, open the server address without any certificate warning and
   without reinstalling anything.** The CA fingerprint on the console is unchanged.
6. [ ] Search for an old message and open an old file URL: nothing is found; the files directory on the host contains no old files
   (`dir data\files`).
7. [ ] Repeat the factory reset with *Generate new CA* **on**: the phones now warn about the certificate (expected) until the new CA is installed
   from the onboarding page. Note the fingerprint changed.

Notes: ______________________________________________

## 9. Backup and restore on the Raspberry Pi

- [ ] On the Pi: `./bubbles backup --out ~/party.bbk --data-dir ~/bubbles-data`. It asks for a passphrase twice (no echo) and refuses an empty one.
- [ ] `head -c 24 party.bbk` shows `age-encryption.org/v1`; the file does not contain readable message text (`strings party.bbk | grep -i <message>` finds nothing).
- [ ] Stop the server. `./bubbles restore ~/party.bbk --data-dir ~/restored-data` (a fresh directory) asks for the passphrase; a wrong passphrase fails.
- [ ] Restoring into a non-empty data directory is refused without `--force`.
- [ ] Start `./bubbles serve --data-dir ~/restored-data` on other ports: the same accounts, channels, messages and files are present; phones that trust
  the same CA connect without a warning; sessions still work.
- [ ] `ls -l ~/restored-data/tls/` shows the key files as `-rw-------`; the directory is `drwx------`.
- [ ] Admin, Danger zone, "Download encrypted backup" produces a file that restores the same way.

Notes: ______________________________________________

## 10. Service install on each OS

For each host, install the service, reboot, and confirm the server is back without a login.

| Host | Command | Starts at boot | Survives crash (kill the process) | Uninstall clean |
|------|---------|:--------------:|:---------------------------------:|:---------------:|
| Windows 11 (elevated terminal) | `bubbles.exe service install` then `service start` | [ ] | [ ] | [ ] |
| Raspberry Pi OS (systemd, `sudo`) | `sudo ./bubbles service install` then `service start` | [ ] | [ ] | [ ] |
| macOS (launchd, `sudo`) | `sudo ./bubbles service install` then `service start` | [ ] | [ ] | [ ] |

- [ ] `bubbles service status` reports running on each host.
- [ ] The service data directory is the system one (`%ProgramData%\bubbles`, `/var/lib/bubbles`, `/Library/Application Support/bubbles`) and is not readable by a standard user.
- [ ] Log files rotate and contain no message text (`grep` the log for a recent message: nothing).
- [ ] `service uninstall` removes the service but leaves the data directory.

Notes: ______________________________________________

## 11. Safari: factory reset with an active service worker

Real Safari (macOS and iOS) has its own service worker and storage behaviour that Playwright's WebKit does not fully reproduce.

- [ ] In Safari on the Mac, sign in, open a channel, then Develop, Service Workers lists the bubbles worker as activated.
- [ ] Perform a factory reset from another browser. In Safari the open tab shows "This server was reset" and navigates to the start page.
- [ ] Reload: the sign-in page appears, not the old app shell; no stale channel list; Develop, Storage shows cleared bubbles data.
- [ ] Repeat on the iPhone, with the app added to the Home Screen: after the reset, opening it shows the sign-in page, not old messages.
- [ ] Unregister is not needed: the new instance is reached with the existing worker updating itself ("New version available" prompt may appear once).

Notes: ______________________________________________

## 12. Offline reload behaviour

- [ ] With the phone connected, turn on airplane mode and reload the page: the offline shell page appears with a clear message and a retry button
  (no browser error page, no stale messages shown as current).
- [ ] Turn airplane mode off and tap retry: the app loads and catches up (resume) with no duplicate messages.
- [ ] Stop the server for a minute while a browser tab is open: the connection banner appears, drafts in the composer are kept, and after the server returns the tab reconnects by itself.
- [ ] Start a new server version while a tab is open: the "New version available - Reload" prompt appears and reloads cleanly.

Notes: ______________________________________________

## 13. Final sign-off

- [ ] Nothing in this list was skipped, or every skip has a reason in its notes.
- [ ] Every failed step has an issue filed with the step number.
- [ ] `docs/security-checklist.md` was re-run on the release build and all items are PASS.

Signed: ______________________  Date: ______________
