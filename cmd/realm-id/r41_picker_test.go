package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// R41 picker: `config set tenant` with MFA, and the step-up retry token.

// r41BFF is a fake BFF that behaves like the real one on the MFA leg: /auth/mfa/verify mints a NEW
// session token and DELETES the bearer's old session, so a later call carrying the old token is a
// 401 session_expired. gated names the route that answers 412 + a challenge for the old session.
type r41BFF struct {
	srv       *httptest.Server
	verifyHit int
	verifyReq map[string]string
	verifyBr  string
	gatedBr   []string // bearers seen on the gated route, in order
	live      map[string]bool
	verifySt  int // 0 = 201 success
}

func newR41BFF(t *testing.T, gated string, verifySt int) *r41BFF {
	t.Helper()
	f := &r41BFF{live: map[string]bool{"rsid_old": true}, verifySt: verifySt}
	mux := http.NewServeMux()
	bearer := func(r *http.Request) string { return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") }
	mux.HandleFunc(gated, func(w http.ResponseWriter, r *http.Request) {
		b := bearer(r)
		f.gatedBr = append(f.gatedBr, b)
		if !f.live[b] {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":"session_expired"}}`)
			return
		}
		if b == "rsid_old" {
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = io.WriteString(w, `{"error":{"code":"mfa_required","mfa_challenge_token":"ch1"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"tenant_id":"t-7"}`)
	})
	mux.HandleFunc("/auth/mfa/verify", func(w http.ResponseWriter, r *http.Request) {
		f.verifyHit++
		f.verifyBr = bearer(r)
		_ = json.NewDecoder(r.Body).Decode(&f.verifyReq)
		if f.verifySt != 0 {
			w.WriteHeader(f.verifySt)
			_, _ = io.WriteString(w, `{"error":{"code":"invalid_code"}}`)
			return
		}
		delete(f.live, f.verifyBr)
		f.live["rsid_new"] = true
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"status":"ok","session_token":"rsid_new","tenant_id":"t-7"}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("REALM_ID_BFF", f.srv.URL)
	t.Setenv("REALM_ID_API_KEY", "")
	if err := saveConfig(&Config{SessionToken: "rsid_old", Tenant: "t-old"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return f
}

// Cites cli README `config set tenant`.
func TestConfigSetTenant_MFA_PromptsVerifiesAdopts(t *testing.T) {
	f := newR41BFF(t, "/switch-tenant", 0)
	_, _, code := captureStd(t, func() int { return cmdConfig([]string{"set", "tenant", "t-7", "--totp", "123456"}) })
	if code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if f.verifyHit != 1 || f.verifyReq["mfa_challenge_token"] != "ch1" || f.verifyReq["code"] != "123456" || f.verifyReq["method"] != "totp" {
		t.Fatalf("verify hit %d with %v", f.verifyHit, f.verifyReq)
	}
	saved, _ := loadConfig()
	if saved.SessionToken != "rsid_new" || saved.Tenant != "t-7" {
		t.Fatalf("saved session=%q tenant=%q, want rsid_new / t-7", saved.SessionToken, saved.Tenant)
	}
}

func TestConfigSetTenant_MFA_NoTerminalNoCode_Exit3(t *testing.T) {
	f := newR41BFF(t, "/switch-tenant", 0)
	_, stderr, code := captureStd(t, func() int { return cmdConfig([]string{"set", "tenant", "t-7"}) })
	if code != exitIncomplete {
		t.Fatalf("exit = %d, want %d", code, exitIncomplete)
	}
	if !strings.Contains(stderr, "needs a TOTP code") {
		t.Fatalf("stderr = %q", stderr)
	}
	if f.verifyHit != 0 {
		t.Fatalf("verify called %d times without a code", f.verifyHit)
	}
	saved, _ := loadConfig()
	if saved.SessionToken != "rsid_old" || saved.Tenant != "t-old" {
		t.Fatalf("config changed: %+v", saved)
	}
}

func TestConfigSetTenant_MFA_WrongCode_ChangesNothing(t *testing.T) {
	_ = newR41BFF(t, "/switch-tenant", http.StatusUnauthorized)
	_, _, code := captureStd(t, func() int { return cmdConfig([]string{"set", "tenant", "t-7", "--totp", "000000"}) })
	if code == exitOK {
		t.Fatal("a wrong code must not exit 0")
	}
	saved, _ := loadConfig()
	if saved.SessionToken != "rsid_old" || saved.Tenant != "t-old" {
		t.Fatalf("config changed: %+v", saved)
	}
}

// Cites cli README step-up: the retry carries the verify body's session_token.
func TestStepUpCall_RetryUsesAdoptedToken(t *testing.T) {
	f := newR41BFF(t, "/api/things", 0)
	var st, abort int
	captureStd(t, func() int {
		st, _, abort = stepUpCall(&Config{SessionToken: "rsid_old", BFFURL: f.srv.URL}, http.MethodPost, "/things", map[string]string{"a": "b"}, "123456")
		return 0
	})
	if abort != -1 || st != http.StatusOK {
		t.Fatalf("status=%d abort=%d, want 200 / -1 (retry bearers %v)", st, abort, f.gatedBr)
	}
	if last := f.gatedBr[len(f.gatedBr)-1]; last != "rsid_new" {
		t.Fatalf("retry bearer = %q, want rsid_new", last)
	}
	saved, _ := loadConfig()
	if saved.SessionToken != "rsid_new" {
		t.Fatalf("saved session = %q, want rsid_new", saved.SessionToken)
	}
}
