package auth

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"errors"
	"strings"
)

// commonPasswordsGz is the SecLists 10k-most-common.txt list, gzipped (see
// docs/DEPENDENCIES.md for source, checksum and license).
//
//go:embed commonpw.txt.gz
var commonPasswordsGz []byte

// commonPasswordsLongGz is a second list for passwords of 10 or more
// characters, which the 10k list barely covers (51 entries). Source:
// SecLists Passwords/Common-Credentials/100k-most-used-passwords-NCSC.txt
// (https://raw.githubusercontent.com/danielmiessler/SecLists/master/Passwords/Common-Credentials/100k-most-used-passwords-NCSC.txt),
// source SHA-256 c2e5696882c603b76bb67a47ee970897e5a76fc4c3f5547abe3d0ca340c576e0,
// MIT license (SecLists). Filtered to entries of at least 10 bytes, de-duplicated
// and sorted (9,307 lines, SHA-256 22180a12ef023b170207675b813234b03950e5fb1a6aa1a5b213e1d5b4d405cc),
// then gzipped with gzip -9 -n. See also COMMONPW_SOURCES.txt.
//
//go:embed commonpw_long.txt.gz
var commonPasswordsLongGz []byte

// commonPasswordSet is decoded at package initialization, so a broken embedded
// list stops start-up instead of silently disabling the common-password rule.
var commonPasswordSet = mustLoadCommonPasswords(commonPasswordsGz, commonPasswordsLongGz)

// mustLoadCommonPasswords panics unless gz decodes to a non-empty list.
func mustLoadCommonPasswords(gzs ...[]byte) map[string]struct{} {
	all := make(map[string]struct{}, 20480)
	for _, gz := range gzs {
		set, err := loadCommonPasswords(gz)
		if err != nil {
			panic("auth: embedded common password list: " + err.Error())
		}
		for w := range set {
			all[w] = struct{}{}
		}
	}
	return all
}

// loadCommonPasswords decodes a gzipped, newline-separated word list. A gzip
// or read error, or an empty list, is an error: never a partial result.
func loadCommonPasswords(gz []byte) (map[string]struct{}, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	set := make(map[string]struct{}, 10240)
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		if w := strings.ToLower(strings.TrimSpace(sc.Text())); w != "" {
			set[w] = struct{}{}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(set) == 0 {
		return nil, errors.New("list is empty")
	}
	return set, nil
}

// IsCommonPassword reports whether pw (compared case-insensitively) is in
// the embedded lists: the 10,000 most common passwords plus the common
// passwords of 10 or more characters from the NCSC 100k list.
func IsCommonPassword(pw string) bool {
	_, ok := commonPasswordSet[strings.ToLower(pw)]
	return ok
}
