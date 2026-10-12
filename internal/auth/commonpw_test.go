package auth

import (
	"bytes"
	"compress/gzip"
	"testing"
)

func TestLoadCommonPasswordsFailsClosed(t *testing.T) {
	var emptyGz bytes.Buffer
	zw := gzip.NewWriter(&emptyGz)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"nil":        nil,
		"not gzip":   []byte("password\n123456\n"),
		"truncated":  commonPasswordsGz[:len(commonPasswordsGz)/2],
		"empty list": emptyGz.Bytes(),
	}
	for name, gz := range tests {
		t.Run(name, func(t *testing.T) {
			set, err := loadCommonPasswords(gz)
			if err == nil || set != nil {
				t.Errorf("got %d words and err %v; want no list and an error", len(set), err)
			}
		})
	}
}

func TestMustLoadCommonPasswordsPanicsOnBadData(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a broken embedded list did not panic")
		}
	}()
	mustLoadCommonPasswords([]byte("not gzip"))
}

func TestLongCommonPasswordsRejected(t *testing.T) {
	for _, pw := range []string{"password123", "1234567890", "qwertyuiop", "iloveyou12", "ABCDEFGHIJ"} {
		if !IsCommonPassword(pw) {
			t.Errorf("IsCommonPassword(%q) = false", pw)
		}
	}
	if IsCommonPassword("correct horse battery staple") {
		t.Error("passphrase flagged as common")
	}
}
