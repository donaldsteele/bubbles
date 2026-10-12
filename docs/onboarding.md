# Onboarding devices

bubbles serves all app traffic over HTTPS with a certificate from its own local
certificate authority (CA). Every device has to trust that CA once. After that
the browser opens bubbles without warnings, and the web app can be installed
to the home screen.

Nothing here needs internet access. The CA only vouches for names and addresses
on your local network (`.local`, `.lan`, `.home.arpa`, and private IP
addresses), and the CA certificate can be downloaded from the server itself.

## Before you start

1. Start the server on a computer on the network and keep its console open.
   It prints the addresses to use and the SHA-256 fingerprint of the CA.
2. On a device, open the **onboarding page** on the server's plain-HTTP port,
   for example `http://192.168.1.20:28226` (or `http://bubbles.local:28226`).
   The page only serves the CA certificate and instructions. Every other path
   redirects to HTTPS.
3. The page shows the instance name, the HTTPS URL, a QR code, and the CA
   fingerprint in groups of four characters. It also detects your operating
   system and shows the matching steps first. All sections are expandable.

### Verify the fingerprint

Before you trust the CA, compare its fingerprint with the one shown on the
server console or on the admin overview page. If they differ, do not install
the certificate. The fingerprint is the only thing that proves the certificate
came from your server and not from someone else on the network.

### Certificate file names

The onboarding page offers:

- **Download certificate**: `bubbles-ca.crt` (DER)
- **iPhone / iPad profile**: `bubbles-ca.mobileconfig`
- **PEM file**: `bubbles-ca.pem`

Your administrator can also export the CA for device management with
`bubbles cert export-ca --format pem|der|mobileconfig --out FILE`.

## Install the CA per device

### iPhone and iPad

1. Open the onboarding page in Safari and tap **iPhone / iPad profile**. Allow the download.
2. Go to **Settings > General > VPN & Device Management**, tap the bubbles
   profile, then tap **Install**.
3. Go to **Settings > General > About > Certificate Trust Settings** and turn on
   full trust for the bubbles CA.

iOS needs both steps. Installing the profile alone does not make Safari trust
the certificate.

### Android

1. Tap **Download certificate** on the onboarding page.
2. Go to **Settings > Security > Encryption & credentials > Install a certificate >
   CA certificate**. Menu names vary by manufacturer; search Settings for
   "CA certificate" if you cannot find it.
3. Choose `bubbles-ca.crt` from Downloads and confirm.

Some Android Chrome builds cannot resolve `.local` names. Use the numeric
address from the onboarding page (for example `https://192.168.1.20:28225`) or
scan the QR code, which uses the IP address.

### Windows

1. Download `bubbles-ca.crt`, then double-click it.
2. Click **Install Certificate**, choose **Current User**, then **Place all
   certificates in the following store** and select **Trusted Root Certification
   Authorities**. Finish the wizard.
3. Restart the browser. Edge, Chrome and Firefox use the Windows store.

Or in PowerShell:

```powershell
Import-Certificate -FilePath "$env:USERPROFILE\Downloads\bubbles-ca.crt" -CertStoreLocation Cert:\CurrentUser\Root
```

### macOS

Terminal:

```sh
security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db ~/Downloads/bubbles-ca.crt
```

Or double-click the file, find the certificate in **Keychain Access**, open it,
expand **Trust**, and set **When using this certificate** to **Always Trust**.
Restart the browser.

### Linux

Download `bubbles-ca.crt`, then:

- Debian and Ubuntu:

  ```sh
  sudo cp bubbles-ca.crt /usr/local/share/ca-certificates/
  sudo update-ca-certificates
  ```

- Fedora, Arch and openSUSE:

  ```sh
  sudo trust anchor --store bubbles-ca.crt
  ```

- Chrome and Chromium use the system store on most distributions. If they still
  warn, go to **Settings > Privacy and security > Security > Manage certificates**.

### Firefox on Linux

Firefox keeps its own certificate store on Linux (Windows and macOS builds use
the system store by default).

1. Go to **Settings > Privacy & Security > Certificates > View Certificates >
   Authorities > Import**.
2. Pick `bubbles-ca.crt` and tick **Trust this CA to identify websites**.

## Open bubbles

1. Scan the QR code on the onboarding page or on the server with the target
   device, or type the HTTPS address.
2. If you have an invitation link, open it. It shows the CA step first, then
   the invitation button and a QR code for `https://…/invite/<code>`.
3. Sign in, or create your account from the invitation.

To install the web app to the home screen, use **Share > Add to Home Screen**
on iOS, or the browser's install option on Android, Windows, macOS and Linux.

## Cannot connect?

- **Client isolation.** Guest, hotel and event Wi-Fi often stops devices from
  talking to each other. A device on such a network cannot reach the server at
  all, even though it has the certificate. Join a network without isolation, or
  ask the network operator to turn it off. The server console logs a hint when
  no other device has connected within ten minutes of setup.
- **`.local` does not resolve.** Some networks block multicast DNS, and some
  Android browsers cannot resolve `.local`. Use the numeric address instead
  (for example `https://192.168.1.20:28225`). The QR code always uses the IP
  address.
- **Certificate warning on the page.** The CA is not trusted on this device yet,
  or the trust step was skipped (iOS Certificate Trust Settings, Firefox import).
  Repeat the steps above and restart the browser.
- **Fingerprint mismatch.** Stop and check with the administrator. Someone else
  may be serving the network, or the server was reset with a new CA. See
  [admin.md](admin.md#reset) for how old CAs are removed.

## Removing an old CA

After a factory reset with **Generate new CA**, devices that trusted the old
CA still have it installed. Its private key was destroyed, so it can no longer
sign anything, but you may want to remove it. Names and menus vary by version.

- **iPhone / iPad:** Settings > General > VPN & Device Management, tap the
  bubbles profile, then **Remove Profile**.
- **Android:** Settings > Security > Encryption & credentials > Trusted
  credentials (User tab), select the bubbles CA, then remove it.
- **Windows:** run `certmgr.msc`, open **Trusted Root Certification Authorities >
  Certificates** under Current User, and delete the certificate whose subject
  starts with `bubbles Local CA`.
- **macOS:** open **Keychain Access**, select the **login** keychain, find the
  `bubbles Local CA` certificate, and delete it.
- **Debian and Ubuntu:** `sudo rm /usr/local/share/ca-certificates/bubbles-ca.crt && sudo update-ca-certificates --fresh`
- **Fedora, Arch and openSUSE:** `sudo trust anchor --remove bubbles-ca.crt`
- **Firefox on Linux:** Settings > Privacy & Security > Certificates > View
  Certificates > Authorities, select the bubbles CA, then **Delete or Distrust**.

After removing an old CA, install the new one from the onboarding page.
