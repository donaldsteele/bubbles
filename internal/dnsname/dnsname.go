// Package dnsname holds the one DNS-name validator shared by configuration
// parsing and the local CA's name constraints.
package dnsname

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// Valid reports whether name is a lower-case DNS name made of dot-separated
// labels of 1-63 characters [a-z0-9-] that neither start nor end with a
// hyphen, at most 253 characters in total, and not an IP literal. The error
// says why it is not.
func Valid(name string) error {
	if name == "" {
		return errors.New("empty name")
	}
	if len(name) > 253 {
		return errors.New("longer than 253 characters")
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return errors.New("IP literals are not DNS names")
	}
	for _, label := range strings.Split(name, ".") {
		if err := validLabel(label); err != nil {
			return err
		}
	}
	return nil
}

func validLabel(label string) error {
	if label == "" || len(label) > 63 {
		return errors.New("each label must be 1-63 characters")
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return fmt.Errorf("label %q may only contain a-z, 0-9 and '-'", label)
		}
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return fmt.Errorf("label %q must not start or end with '-'", label)
	}
	return nil
}
