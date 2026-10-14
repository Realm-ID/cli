package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// TestErrorReferenceMatchesItsPin: the embedded error-reference.md must be the
// bytes ERROR_REFERENCE_CONTRACT names (scripts/revendor-error-reference.sh).
func TestErrorReferenceMatchesItsPin(t *testing.T) {
	raw, err := os.ReadFile("ERROR_REFERENCE_CONTRACT")
	if err != nil {
		t.Fatalf("ERROR_REFERENCE_CONTRACT unreadable (%v); re-vendor with scripts/revendor-error-reference.sh <sdk sha>", err)
	}
	meta := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("ERROR_REFERENCE_CONTRACT: unparseable line %q", line)
		}
		meta[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if len(meta["sdk_sha"]) != 40 {
		t.Fatalf("sdk_sha %q is not a 40-hex commit", meta["sdk_sha"])
	}
	sum := sha256.Sum256(errorReferenceMD)
	if got := hex.EncodeToString(sum[:]); got != meta["sha256"] {
		t.Fatalf("embedded error-reference.md sha256 %s != pinned %s: hand-edit of one file, or a re-vendor that missed the other", got, meta["sha256"])
	}
	if errorReferenceSHA() != meta["sdk_sha"] {
		t.Fatalf("errorReferenceSHA() %q != pin %q", errorReferenceSHA(), meta["sdk_sha"])
	}
}
