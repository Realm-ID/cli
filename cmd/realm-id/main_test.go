package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	if code := authLogin(cfg); code != exitOK {
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

	if code := authLogin(&Config{}); code != exitForbidden {
		t.Fatalf("authLogin exit = %d, want exitForbidden (%d)", code, exitForbidden)
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
	if code := authLogin(cfg); code != exitOK {
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
	if code := authLogin(cfg); code != exitOK {
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
	if code := authLogin(cfg); code != exitOK {
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
