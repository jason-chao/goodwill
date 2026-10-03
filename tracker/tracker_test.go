package tracker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"testing"
)

// The committed script must be the file VERSION describes. This fails if
// script.js is edited by hand or rebuilt without updating VERSION.
func TestScriptMatchesRecordedChecksum(t *testing.T) {
	version, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^sha256: ([0-9a-f]{64})$`).FindSubmatch(version)
	if m == nil {
		t.Fatal("VERSION has no sha256 line")
	}
	sum := sha256.Sum256(Script)
	if got := hex.EncodeToString(sum[:]); got != string(m[1]) {
		t.Errorf("script.js sha256 is %s, VERSION records %s", got, m[1])
	}
	if !regexp.MustCompile(`(?m)^tag: v\d+\.\d+\.\d+$`).Match(version) {
		t.Error("VERSION does not name a release tag")
	}
	if !bytes.Contains(Script, []byte("/api/send")) {
		t.Error("script.js does not post to /api/send")
	}
}
