package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A saved config is 0600 whether or not the file already existed.
func TestSaveConfigTightensExistingMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "realm-id", "config.json")
	t.Setenv("REALM_ID_CONFIG", p)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(&Config{SessionToken: "tok"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
}

// The token must never be written into a file with a wider mode: saving over an
// existing 0644 config replaces it (new inode) rather than writing in place. A
// hard link to the old file stands in for a reader who already holds it open;
// it must never see the token.
func TestSaveConfigNeverWritesTokenIntoWiderFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "realm-id", "config.json")
	t.Setenv("REALM_ID_CONFIG", p)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "old-link")
	if err := os.Link(p, old); err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(&Config{SessionToken: "tok-secret"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{}" {
		t.Fatalf("old 0644 file was written in place, now holds %q", b)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("new config: err=%v info=%v, want mode 600", err, fi)
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(p), "*.tmp*"))
	if len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}
