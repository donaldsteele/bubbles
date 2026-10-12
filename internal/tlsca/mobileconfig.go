package tlsca

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"strings"
)

// MobileConfigContentType is the MIME type iOS and macOS expect for profiles.
const MobileConfigContentType = "application/x-apple-aspen-config"

// MobileConfig returns an unsigned Apple configuration profile that installs
// the CA as a root certificate. Identifiers and UUIDs derive from the CA
// fingerprint, so downloading the profile twice replaces rather than
// duplicates it on the device.
func (ca *CA) MobileConfig() []byte {
	sum := sha256.Sum256(ca.Cert.Raw)
	id := "local.bubbles.ca." + hex.EncodeToString(sum[:8])
	name := ca.Cert.Subject.CommonName

	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PayloadContent</key>
	<array>
		<dict>
`)
	plistString(&b, 3, "PayloadCertificateFileName", "bubbles-ca.crt")
	fmt.Fprintf(&b, "\t\t\t<key>PayloadContent</key>\n\t\t\t<data>%s</data>\n", base64.StdEncoding.EncodeToString(ca.Cert.Raw))
	plistString(&b, 3, "PayloadDescription", "Adds the bubbles local certificate authority as a trusted root.")
	plistString(&b, 3, "PayloadDisplayName", name)
	plistString(&b, 3, "PayloadIdentifier", id+".cert")
	plistString(&b, 3, "PayloadType", "com.apple.security.root")
	plistString(&b, 3, "PayloadUUID", derivedUUID(sum[:], "cert"))
	b.WriteString("\t\t\t<key>PayloadVersion</key>\n\t\t\t<integer>1</integer>\n\t\t</dict>\n\t</array>\n")
	plistString(&b, 1, "PayloadDescription", "Lets this device open bubbles over HTTPS. Fingerprint (SHA-256): "+ca.Fingerprint())
	plistString(&b, 1, "PayloadDisplayName", name)
	plistString(&b, 1, "PayloadIdentifier", id)
	b.WriteString("\t<key>PayloadRemovalDisallowed</key>\n\t<false/>\n")
	plistString(&b, 1, "PayloadType", "Configuration")
	plistString(&b, 1, "PayloadUUID", derivedUUID(sum[:], "profile"))
	b.WriteString("\t<key>PayloadVersion</key>\n\t<integer>1</integer>\n</dict>\n</plist>\n")
	return b.Bytes()
}

func plistString(b *bytes.Buffer, indent int, key, value string) {
	tabs := strings.Repeat("\t", indent)
	fmt.Fprintf(b, "%s<key>%s</key>\n%s<string>", tabs, key, tabs)
	_ = xml.EscapeText(b, []byte(value))
	b.WriteString("</string>\n")
}

// derivedUUID returns a version-5-style UUID derived from seed and label.
func derivedUUID(seed []byte, label string) string {
	h := sha256.New()
	h.Write(seed)
	h.Write([]byte(label))
	u := h.Sum(nil)[:16]
	u[6] = (u[6] & 0x0f) | 0x50
	u[8] = (u[8] & 0x3f) | 0x80
	x := strings.ToUpper(hex.EncodeToString(u))
	return x[0:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:32]
}
