package main

import (
	"net/http"
	"testing"
)

func TestErrorCode(t *testing.T) {
	if got := errorCode([]byte(`{"error":{"code":"authorization_pending","message":"x"}}`)); got != "authorization_pending" {
		t.Fatalf("errorCode = %q", got)
	}
	if got := errorCode([]byte(`not json`)); got != "" {
		t.Fatalf("errorCode on garbage = %q, want empty", got)
	}
}

func TestExitForStatus(t *testing.T) {
	cases := map[int]int{
		200:                            exitOK,
		http.StatusUnauthorized:        exitForbidden,
		http.StatusForbidden:           exitForbidden,
		http.StatusNotFound:            exitNotFound,
		http.StatusConflict:            exitConflict,
		http.StatusInternalServerError: exitErr,
	}
	for st, want := range cases {
		if got := exitForStatus(st); got != want {
			t.Fatalf("status %d → %d, want %d", st, got, want)
		}
	}
}

func TestRunDispatch(t *testing.T) {
	if run([]string{"version"}) != exitOK {
		t.Fatal("version should exit 0")
	}
	if run(nil) != exitUsage {
		t.Fatal("no args should be usage error")
	}
	if run([]string{"bogus-cmd"}) != exitUsage {
		t.Fatal("unknown command should be usage error")
	}
}

func TestConfigValueDefaults(t *testing.T) {
	c := &Config{Platform: "plt_x"}
	if configValue(c, "platform") != "plt_x" {
		t.Fatal("platform")
	}
	if configValue(c, "bff_url") != defaultBFFURL {
		t.Fatalf("bff_url default = %q", configValue(c, "bff_url"))
	}
	if configValue(c, "nope") != "" {
		t.Fatal("unknown key should be empty")
	}
}
