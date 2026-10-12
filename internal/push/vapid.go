package push

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// VAPIDFile is the name of the VAPID key file inside the data directory.
const VAPIDFile = "vapid.json"

// VAPID is the server's application server key pair (RFC 8292). Both keys
// are base64url without padding: the private key is the 32-byte P-256 scalar,
// the public key the 65-byte uncompressed point browsers take as
// applicationServerKey.
type VAPID struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

// LoadOrCreateVAPID reads <dataDir>/vapid.json, generating and storing a new
// key pair (file mode 0600) when the file does not exist. An existing file
// that is unreadable or inconsistent is an error, never silently replaced: a
// new key pair would invalidate every browser subscription.
func LoadOrCreateVAPID(dataDir string) (VAPID, error) {
	path := filepath.Join(dataDir, VAPIDFile)
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		return parseVAPID(raw)
	case errors.Is(err, os.ErrNotExist):
	default:
		return VAPID{}, fmt.Errorf("read %s: %w", VAPIDFile, err)
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return VAPID{}, fmt.Errorf("generate VAPID keys: %w", err)
	}
	v := VAPID{PrivateKey: priv, PublicKey: pub}
	if err := v.check(); err != nil {
		return VAPID{}, fmt.Errorf("generated VAPID keys: %w", err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return VAPID{}, err
	}
	if err := writeFileAtomic(path, append(data, '\n')); err != nil {
		return VAPID{}, fmt.Errorf("write %s: %w", VAPIDFile, err)
	}
	return v, nil
}

func parseVAPID(raw []byte) (VAPID, error) {
	var v VAPID
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return VAPID{}, fmt.Errorf("parse %s: %w", VAPIDFile, err)
	}
	if err := v.check(); err != nil {
		return VAPID{}, fmt.Errorf("%s: %w", VAPIDFile, err)
	}
	return v, nil
}

// check verifies that the private key is a valid P-256 scalar and that the
// public key is its point.
func (v VAPID) check() error {
	privBytes, err := base64.RawURLEncoding.DecodeString(v.PrivateKey)
	if err != nil {
		return errors.New("private key is not base64url")
	}
	priv, err := ecdh.P256().NewPrivateKey(privBytes)
	if err != nil {
		return errors.New("private key is not a P-256 key")
	}
	pubBytes, err := base64.RawURLEncoding.DecodeString(v.PublicKey)
	if err != nil {
		return errors.New("public key is not base64url")
	}
	if !bytes.Equal(priv.PublicKey().Bytes(), pubBytes) {
		return errors.New("public key does not match the private key")
	}
	return nil
}

// writeFileAtomic writes data to a temporary file beside path and renames it
// into place. os.CreateTemp creates the file 0600; on Windows the data
// directory's protected ACL is inherited.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}
