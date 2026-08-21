package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestErrorCode(t *testing.T) {
	if got := errorCode([]byte(`{"error":{"code":"authorization_pending","message":"x"}}`)); got != "authorization_pending" {
		t.Fatalf("errorCode = %q", got)
	}
	if got := errorCode([]byte(`not json`)); got != "" {
		t.Fatalf("errorCode on garbage = %q, want empty", got)
	}
}

// TestDecodeBody locks the GoFr-envelope contract: native BFF handlers wrap
// their return in {"data":{…}} (gofr http/response.Response), so decodeBody
// must peel that off, while still decoding a bare/passthrough body unchanged.
func TestDecodeBody(t *testing.T) {
	type payload struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}

	var wrapped payload
	decodeBody([]byte(`{"data":{"device_code":"dc","user_code":"uc"}}`), &wrapped)
	if wrapped.DeviceCode != "dc" || wrapped.UserCode != "uc" {
		t.Fatalf("envelope decode = %+v, want {dc uc}", wrapped)
	}

	var bare payload
	decodeBody([]byte(`{"device_code":"dc2","user_code":"uc2"}`), &bare)
	if bare.DeviceCode != "dc2" || bare.UserCode != "uc2" {
		t.Fatalf("bare decode = %+v, want {dc2 uc2}", bare)
	}

	var garbage payload
	decodeBody([]byte(`not json`), &garbage) // must not panic; leaves zero value
	if garbage.DeviceCode != "" || garbage.UserCode != "" {
		t.Fatalf("garbage decode = %+v, want zero", garbage)
	}
}

// TestAuthLogin_DeviceFlow drives the whole device-grant round-trip against a
// fake BFF that renders bodies exactly as the real one does: success under the
// GoFr {"data":…} envelope and RFC-8628 poll errors under {"error":{"code"}}.
// It regression-locks the envelope unwrap on BOTH endpoints — the device_code
// the CLI feeds back when polling, and the session_token it finally persists.
func TestAuthLogin_DeviceFlow(t *testing.T) {
	var codePosts, tokenPosts int
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/device/code", func(w http.ResponseWriter, _ *http.Request) {
		codePosts++
		_, _ = io.WriteString(w, `{"data":{"device_code":"dvc_abc","user_code":"WXYZ-1234",`+
			`"verification_uri":"https://app.example/device",`+
			`"verification_uri_complete":"https://app.example/device?user_code=WXYZ-1234",`+
			`"expires_in":60,"interval":1}}`)
	})
	mux.HandleFunc("/auth/device/token", func(w http.ResponseWriter, r *http.Request) {
		tokenPosts++
		var body struct {
			DeviceCode string `json:"device_code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.DeviceCode != "dvc_abc" {
			t.Errorf("poll #%d sent device_code=%q, want dvc_abc (envelope not unwrapped on /code?)", tokenPosts, body.DeviceCode)
		}
		if tokenPosts == 1 {
			// RFC-8628 §3.5: still pending — error-envelope shape from middleware.Err.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"authorization_pending","message":"waiting for approval"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"session_token":"sess_xyz","realm_id":"rlm_1"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("REALM_ID_BFF", srv.URL)

	cfg := &Config{}
	if code := authLogin(cfg, "test-host"); code != exitOK {
		t.Fatalf("authLogin exit = %d, want exitOK (%d)", code, exitOK)
	}
	if cfg.SessionToken != "sess_xyz" {
		t.Fatalf("cfg.SessionToken = %q, want sess_xyz (token envelope not unwrapped?)", cfg.SessionToken)
	}
	saved, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if saved.SessionToken != "sess_xyz" {
		t.Fatalf("persisted session_token = %q, want sess_xyz", saved.SessionToken)
	}
	if codePosts != 1 {
		t.Fatalf("device/code posts = %d, want 1", codePosts)
	}
	if tokenPosts < 2 {
		t.Fatalf("device/token posts = %d, want >=2 (pending then approved)", tokenPosts)
	}
}

// TestAuthLogin_SupersededStops verifies that when a newer `auth login` claims
// the active-login marker, an older poller stops on its next tick instead of
// polling its abandoned code for the whole TTL (ADR-062 §2). The fake BFF
// rewrites the marker to a different device_code on the first poll, simulating
// a second login starting in another terminal.
func TestAuthLogin_SupersededStops(t *testing.T) {
	var polls int
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/device/code", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"device_code":"dvc_old","user_code":"WXYZ-1234",`+
			`"verification_uri":"https://app.example/device","expires_in":60,"interval":1}}`)
	})
	mux.HandleFunc("/auth/device/token", func(w http.ResponseWriter, _ *http.Request) {
		polls++
		// A newer login starts: overwrite the shared marker.
		if p, err := activeLoginPath(); err == nil {
			_ = os.WriteFile(p, []byte("dvc_new"), 0o600)
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"authorization_pending","message":"pending"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("REALM_ID_BFF", srv.URL)

	if code := authLogin(&Config{}, "test-host"); code != exitErr {
		t.Fatalf("authLogin exit = %d, want exitErr (%d) on supersession", code, exitErr)
	}
	if polls != 1 {
		t.Fatalf("device/token polls = %d, want 1 (should stop on the tick after supersession)", polls)
	}
}

// TestAuthLogin_AccessDenied confirms the {"error":{"code":"access_denied"}}
// envelope is recognized and maps to the forbidden exit, rather than spinning
// to the timeout.
func TestAuthLogin_AccessDenied(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/device/code", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"device_code":"dvc_abc","user_code":"WXYZ-1234",`+
			`"verification_uri":"https://app.example/device","expires_in":60,"interval":1}}`)
	})
	mux.HandleFunc("/auth/device/token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"access_denied","message":"denied"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("REALM_ID_BFF", srv.URL)

	if code := authLogin(&Config{}, "test-host"); code != exitForbidden {
		t.Fatalf("authLogin exit = %d, want exitForbidden (%d)", code, exitForbidden)
	}
}

// captureStderr redirects os.Stderr for the duration of fn and returns whatever
// was written there — used to assert the user-facing failure message.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// TestAuthLogin_ApprovalFailed exercises the poll loop's `default` branch: a
// terminal approve-side error code (e.g. approval_needs_app) is surfaced with
// its real message, not masqueraded as an "expired"/"timed out" failure.
func TestAuthLogin_ApprovalFailed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/device/code", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"device_code":"dvc_abc","user_code":"WXYZ-1234",`+
			`"verification_uri":"https://app.example/device","expires_in":60,"interval":1}}`)
	})
	mux.HandleFunc("/auth/device/token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"approval_needs_app",`+
			`"message":"complete MFA/first-login setup in the app"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("REALM_ID_BFF", srv.URL)

	var code int
	out := captureStderr(t, func() { code = authLogin(&Config{}, "test-host") })

	if code != exitErr {
		t.Fatalf("authLogin exit = %d, want exitErr (%d) on approval failure", code, exitErr)
	}
	if !strings.Contains(out, "complete MFA/first-login setup in the app") {
		t.Fatalf("stderr = %q, want the real approval-failure message", out)
	}
	if strings.Contains(out, "expired") || strings.Contains(out, "timed out") {
		t.Fatalf("stderr = %q, approval failure must not masquerade as expired/timed out", out)
	}
}

// TestAuthLogin_ApprovalFailed_EmptyMessage hits the `approval failed (<code>)`
// fallback when the envelope carries a code but no human-readable message.
func TestAuthLogin_ApprovalFailed_EmptyMessage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/device/code", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"device_code":"dvc_abc","user_code":"WXYZ-1234",`+
			`"verification_uri":"https://app.example/device","expires_in":60,"interval":1}}`)
	})
	mux.HandleFunc("/auth/device/token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"login_failed","message":""}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("REALM_ID_BFF", srv.URL)

	var code int
	out := captureStderr(t, func() { code = authLogin(&Config{}, "test-host") })

	if code != exitErr {
		t.Fatalf("authLogin exit = %d, want exitErr (%d)", code, exitErr)
	}
	if !strings.Contains(out, "approval failed (login_failed)") {
		t.Fatalf("stderr = %q, want the code-fallback message", out)
	}
	if strings.Contains(out, "expired") || strings.Contains(out, "timed out") {
		t.Fatalf("stderr = %q, must not masquerade as expired/timed out", out)
	}
}

// deviceLoginMux builds a fake BFF that approves immediately, returning the
// given device-token poll body (already wrapped in the GoFr {"data":…}
// envelope), and records whether /switch-tenant was called and with what.
func deviceLoginMux(t *testing.T, tokenBody string) (*http.ServeMux, *string) {
	t.Helper()
	var switched string
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/device/code", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"device_code":"dvc_abc","user_code":"WXYZ-1234",`+
			`"verification_uri":"https://app.example/device","expires_in":60,"interval":1}}`)
	})
	mux.HandleFunc("/auth/device/token", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, tokenBody)
	})
	mux.HandleFunc("/switch-tenant", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			TenantID string `json:"tenant_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switched = body.TenantID
		_, _ = io.WriteString(w, `{"data":{"expires_at":123}}`)
	})
	return mux, &switched
}

// TestAuthLogin_SingleTenant_AutoPicks: an unpinned single-membership login
// auto-selects the tenant and pins it via /switch-tenant (ADR-062 §2).
func TestAuthLogin_SingleTenant_AutoPicks(t *testing.T) {
	mux, switched := deviceLoginMux(t, `{"data":{"session_token":"sess_xyz","realm_id":"rlm_1",`+
		`"tenant_id":"","tenants":[{"id":"t-1","display_name":"Acme"}]}}`)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("REALM_ID_BFF", srv.URL)

	cfg := &Config{}
	if code := authLogin(cfg, "test-host"); code != exitOK {
		t.Fatalf("authLogin exit = %d", code)
	}
	if *switched != "t-1" {
		t.Fatalf("/switch-tenant called with %q, want t-1", *switched)
	}
	saved, _ := loadConfig()
	if saved.Tenant != "t-1" {
		t.Fatalf("persisted tenant = %q, want t-1", saved.Tenant)
	}
}

// TestAuthLogin_MultiTenant_ListsNoSwitch: a multi-membership login leaves the
// session unpinned — no /switch-tenant, no persisted tenant — and lists choices.
func TestAuthLogin_MultiTenant_ListsNoSwitch(t *testing.T) {
	mux, switched := deviceLoginMux(t, `{"data":{"session_token":"sess_xyz","realm_id":"rlm_1",`+
		`"tenant_id":"","tenants":[{"id":"t-1"},{"id":"t-2"}]}}`)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("REALM_ID_BFF", srv.URL)

	cfg := &Config{}
	if code := authLogin(cfg, "test-host"); code != exitOK {
		t.Fatalf("authLogin exit = %d", code)
	}
	if *switched != "" {
		t.Fatalf("/switch-tenant should not be called for multi-tenant, got %q", *switched)
	}
	saved, _ := loadConfig()
	if saved.Tenant != "" {
		t.Fatalf("multi-tenant login must leave tenant unpinned, got %q", saved.Tenant)
	}
}

// TestAuthLogin_Pinned_RecordsTenant: a BFF-pinned (single-tenant) login records
// the returned tenant_id without an extra /switch-tenant round-trip.
func TestAuthLogin_Pinned_RecordsTenant(t *testing.T) {
	mux, switched := deviceLoginMux(t, `{"data":{"session_token":"sess_xyz","realm_id":"rlm_1",`+
		`"tenant_id":"t-9","tenants":[{"id":"t-9"}]}}`)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("REALM_ID_BFF", srv.URL)

	cfg := &Config{}
	if code := authLogin(cfg, "test-host"); code != exitOK {
		t.Fatalf("authLogin exit = %d", code)
	}
	if *switched != "" {
		t.Fatalf("already-pinned login should not re-switch, got %q", *switched)
	}
	saved, _ := loadConfig()
	if saved.Tenant != "t-9" {
		t.Fatalf("persisted tenant = %q, want t-9", saved.Tenant)
	}
}

// TestConfigSetTenant_Switches: `config set tenant <id>` pins the live session
// via /switch-tenant and persists it.
func TestConfigSetTenant_Switches(t *testing.T) {
	mux, switched := deviceLoginMux(t, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("REALM_ID_BFF", srv.URL)

	if err := saveConfig(&Config{SessionToken: "sess_xyz"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if code := cmdConfig([]string{"set", "tenant", "t-7"}); code != exitOK {
		t.Fatalf("config set tenant exit = %d", code)
	}
	if *switched != "t-7" {
		t.Fatalf("/switch-tenant called with %q, want t-7", *switched)
	}
	saved, _ := loadConfig()
	if saved.Tenant != "t-7" {
		t.Fatalf("persisted tenant = %q, want t-7", saved.Tenant)
	}
}

// TestConfigSetTenant_FailedSwitchNotPersisted: a rejected switch must not
// persist the tenant (the session stays on its prior pin).
func TestConfigSetTenant_FailedSwitchNotPersisted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/switch-tenant", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":"not_a_member","message":"nope"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("REALM_ID_BFF", srv.URL)

	if err := saveConfig(&Config{SessionToken: "sess_xyz", Tenant: "t-old"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if code := cmdConfig([]string{"set", "tenant", "t-bad"}); code == exitOK {
		t.Fatal("config set tenant should fail on a rejected switch")
	}
	saved, _ := loadConfig()
	if saved.Tenant != "t-old" {
		t.Fatalf("failed switch persisted tenant = %q, want t-old unchanged", saved.Tenant)
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

func TestResolveDeviceName(t *testing.T) {
	t.Run("flag wins (space form)", func(t *testing.T) {
		if got := resolveDeviceName([]string{"--device-name", "fromflag"}); got != "fromflag" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("flag wins (equals form)", func(t *testing.T) {
		if got := resolveDeviceName([]string{"--device-name=eqflag"}); got != "eqflag" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("env when no flag", func(t *testing.T) {
		t.Setenv("REALM_ID_DEVICE_NAME", "fromenv")
		if got := resolveDeviceName(nil); got != "fromenv" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("falls back to a non-empty default", func(t *testing.T) {
		t.Setenv("REALM_ID_DEVICE_NAME", "")
		if got := resolveDeviceName(nil); got == "" {
			t.Fatal("expected hostname or fallback, got empty")
		}
	})
}

func TestAcquireLoginLock_Singleton(t *testing.T) {
	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	// First acquire wins.
	release, err := acquireLoginLock(11 * time.Minute)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if release == nil {
		t.Fatal("first acquire returned nil release")
	}

	// Second concurrent acquire is refused with the sentinel.
	if _, err := acquireLoginLock(11 * time.Minute); !errors.Is(err, errLoginInProgress) {
		t.Fatalf("second acquire = %v, want errLoginInProgress", err)
	}

	// After release, a new acquire succeeds again.
	release()
	release2, err := acquireLoginLock(11 * time.Minute)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release2()
}

func TestAcquireLoginLock_ReclaimsStale(t *testing.T) {
	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	// A lock whose deadline is already in the past is stale and reclaimable.
	if _, err := acquireLoginLock(-time.Second); err != nil {
		t.Fatalf("acquire (writes already-expired deadline): %v", err)
	}
	if release, err := acquireLoginLock(11 * time.Minute); err != nil {
		t.Fatalf("expected stale lock to be reclaimed, got %v", err)
	} else {
		release()
	}
}

// TestAuthLogin_Accepts201Created is the regression guard for the device-login
// bug where the CLI required HTTP 200 on the approved /auth/device/token poll
// but the GoFr BFF returns 201 Created — so the delivered session token was
// discarded and every successful approval looked like "expired before approval".
// The poll must accept the whole 2xx class.
func TestAuthLogin_Accepts201Created(t *testing.T) {
	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("REALM_ID_NO_BROWSER", "1") // don't spawn a real browser

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/device/code", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated) // GoFr returns 201 for POST
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"device_code": "dvc_test", "user_code": "TEST-CODE",
			"verification_uri_complete": "https://app.realmid.dev/device?user_code=TEST-CODE",
			"expires_in":                600, "interval": 1,
		}})
	})
	mux.HandleFunc("/auth/device/token", func(w http.ResponseWriter, _ *http.Request) {
		// Approved — return the session token with 201 Created (the GoFr default
		// that the old CLI mishandled).
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"session_token": "rsid_devicelogin", "realm_id": "r-1", "tenant_id": "t-1",
		}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("REALM_ID_BFF", srv.URL)

	cfg := &Config{}
	if rc := authLogin(cfg, "test-device"); rc != exitOK {
		t.Fatalf("authLogin returned %d, want exitOK(%d) — 201 must be accepted", rc, exitOK)
	}
	if cfg.SessionToken != "rsid_devicelogin" {
		t.Fatalf("session token not captured from 201 response: %q", cfg.SessionToken)
	}
}

// captureStd runs fn with os.Stdout and os.Stderr redirected to temp files and
// returns what each received. The CLI writes to the package-level os.Stdout /
// os.Stderr directly, so swapping them is the only way to observe the split —
// and the split IS the contract here (ADR-062: stdout is machine-readable, so a
// human hint must never land there).
func captureStd(t *testing.T, fn func() int) (string, string, int) {
	t.Helper()
	dir := t.TempDir()
	outF, err := os.Create(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatalf("create out: %v", err)
	}
	errF, err := os.Create(filepath.Join(dir, "err"))
	if err != nil {
		t.Fatalf("create err: %v", err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outF, errF
	code := fn()
	os.Stdout, os.Stderr = oldOut, oldErr
	outF.Close()
	errF.Close()
	ob, _ := os.ReadFile(filepath.Join(dir, "out"))
	eb, _ := os.ReadFile(filepath.Join(dir, "err"))
	return string(ob), string(eb), code
}

// TestWhoami_DeadSessionNamesTheRemedy — a CLI whose session has gone must say
// what to DO about it.
//
// Traide hit this provisioning their prod realm (2026-06-29): a long sequence
// 401'd partway through and read as "the CLI broke". The body already carries
// `session_expired`, so the CAUSE was on screen; the remedy was not, and a
// human reading a JSON error envelope mid-script does not infer `auth login`.
//
// Deliberately narrow. The original TODO asked to print a session's remaining
// LIFETIME, which is not buildable (the bearer is ADR-060's opaque id, not a
// JWT, and nothing hands the CLI an expiry) and — verified 2026-08-21 against a
// live stack — would answer a question that no longer bites: the BFF's
// passthrough self-heals an expired access JWT, so a session survives until the
// refresh or idle window ends. This handles the case that DOES end a session.
func TestWhoami_DeadSessionNamesTheRemedy(t *testing.T) {
	for _, code := range []string{"session_expired", "session_missing", "session_revoked"} {
		t.Run(code, func(t *testing.T) {
			body := `{"error":{"code":"` + code + `","message":"session not found or expired"}}`
			mux := http.NewServeMux()
			mux.HandleFunc("/me", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, body)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
			t.Setenv("REALM_ID_BFF", srv.URL)

			stdout, stderr, exit := captureStd(t, func() int {
				return authWhoami(&Config{SessionToken: "sess_dead"})
			})

			if !strings.Contains(stderr, "realm-id auth login") {
				t.Errorf("stderr does not name the remedy.\ngot: %q", stderr)
			}
			if !strings.Contains(stderr, code) {
				t.Errorf("stderr does not name the cause %q.\ngot: %q", code, stderr)
			}
			// ADR-062: stdout is the machine-readable channel. An agent parses
			// it, so the hint must not contaminate it — and the server's body
			// must still arrive intact.
			if !strings.Contains(stdout, body) {
				t.Errorf("stdout lost the server body.\ngot: %q", stdout)
			}
			if strings.Contains(stdout, "auth login") {
				t.Errorf("the human hint leaked into stdout, breaking the JSON contract.\ngot: %q", stdout)
			}
			if exit != exitForbidden {
				t.Errorf("exit = %d, want exitForbidden (%d)", exit, exitForbidden)
			}
		})
	}
}

// TestWhoami_HealthySessionSaysNothing is the POSITIVE CONTROL for the test
// above: an unconditionally-printed hint would satisfy every assertion there.
// A working session must leave stderr untouched, or the CLI cries wolf on every
// call and the hint stops meaning anything.
func TestWhoami_HealthySessionSaysNothing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/me", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"user_id":"u-1"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("REALM_ID_BFF", srv.URL)

	stdout, stderr, exit := captureStd(t, func() int {
		return authWhoami(&Config{SessionToken: "sess_live"})
	})
	if stderr != "" {
		t.Errorf("a healthy session wrote to stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "u-1") {
		t.Errorf("stdout = %q, want the profile body", stdout)
	}
	if exit != exitOK {
		t.Errorf("exit = %d, want exitOK", exit)
	}
}

// TestWhoami_OtherFailuresAreNotSessionHints — a 403 on a permission problem
// must NOT tell the operator to log in again. That advice would send them round
// a loop that cannot fix anything, which is worse than silence: it relabels an
// authorization failure as an authentication one.
func TestWhoami_OtherFailuresAreNotSessionHints(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/me", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":"insufficient_permission","message":"nope"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("REALM_ID_BFF", srv.URL)

	_, stderr, _ := captureStd(t, func() int {
		return authWhoami(&Config{SessionToken: "sess_live"})
	})
	if strings.Contains(stderr, "auth login") {
		t.Errorf("a permission failure was mislabelled as a dead session: %q", stderr)
	}
}

// TestSessionHint_KeysOnBothStatusAndCode closes a gap this test file HAD.
//
// Found by mutation, not by review: dropping the `status != 401` guard and
// dropping the code switch BOTH left the suite green, because every existing
// case varied status and code together. A test set that only ever moves two
// variables in lockstep cannot tell which one the code is reading — so it was
// pinning the conjunction by accident and neither half individually.
//
// The 200 case is not hypothetical for the passthrough: `/api/*` forwards the
// ISSUER's body verbatim, so a successful response can carry nested text that
// looks like an error envelope. Hinting "log in again" on a 200 would be the
// worst outcome of the three — it tells the operator their session is dead at
// the exact moment it demonstrably is not.
func TestSessionHint_KeysOnBothStatusAndCode(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{
			// Catches "hint on ANY 401".
			name:   "401 with a non-session code",
			status: http.StatusUnauthorized,
			body:   `{"error":{"code":"insufficient_permission","message":"nope"}}`,
		},
		{
			// Catches "hint on ANY status". 206 is not a hypothetical: it is
			// this codebase's documented GoFr trap — a handler returning a
			// helper's (typedNilData, err) pair collapses the 4xx into a
			// 206 carrying a real error envelope (issuer/TODO.md, "GoFr
			// typed-nil→206"). So a genuine session_expired body CAN arrive
			// under a success-class status, and hinting there would tell the
			// operator their session is dead at the moment it is not.
			name:   "206 carrying a real session_expired envelope (the GoFr typed-nil trap)",
			status: http.StatusPartialContent,
			body:   `{"error":{"code":"session_expired","message":"session not found or expired"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/me", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
			t.Setenv("REALM_ID_BFF", srv.URL)

			_, stderr, _ := captureStd(t, func() int {
				return authWhoami(&Config{SessionToken: "sess"})
			})
			if strings.Contains(stderr, "auth login") {
				t.Errorf("hinted a dead session on %s.\nstderr: %q", tc.name, stderr)
			}
		})
	}
}
