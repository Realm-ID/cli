package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRunCommand_SessionMode_RoutesThroughBFFApi is the end-to-end regression
// guard for the routing bug: a typed command authenticated by a device-flow
// session token must hit the BFF's /api/* passthrough with the rsid_ bearer —
// not the issuer, which rejects the session token.
func TestRunCommand_SessionMode_RoutesThroughBFFApi(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	t.Setenv("REALM_ID_API_KEY", "") // session mode, not service mode
	t.Setenv("REALM_ID_BFF", srv.URL)
	t.Setenv("REALM_ID_ISSUER", "https://issuer.invalid") // must NOT be hit

	cmd := command{Group: []string{"things"}, Verb: "list", Method: "GET", Path: "/things"}
	cfg := &Config{SessionToken: "rsid_sess"}
	if code := runCommand(cfg, cmd, nil); code != exitOK {
		t.Fatalf("runCommand exit = %d, want exitOK (%d)", code, exitOK)
	}
	if gotPath != "/api/things" {
		t.Fatalf("BFF saw path %q, want /api/things (the passthrough mount)", gotPath)
	}
	if gotAuth != "Bearer rsid_sess" {
		t.Fatalf("BFF saw auth %q, want the session bearer", gotAuth)
	}
}

func TestParseFlags(t *testing.T) {
	pf, err := parseFlags([]string{"--platform", "plt_1", "--tenant=t_2", "--field", "name=x", "--field", "n:=3", "--output", "table"})
	if err != nil {
		t.Fatal(err)
	}
	if pf.vals["platform"] != "plt_1" || pf.vals["tenant"] != "t_2" || pf.vals["output"] != "table" {
		t.Fatalf("vals = %+v", pf.vals)
	}
	if len(pf.fields) != 2 {
		t.Fatalf("fields = %v", pf.fields)
	}
	if _, err := parseFlags([]string{"positional"}); err == nil {
		t.Error("bare positional arg should error")
	}
	if _, err := parseFlags([]string{"--platform"}); err == nil {
		t.Error("value-flag without value should error")
	}
}

func TestResolveParam(t *testing.T) {
	cfg := &Config{Platform: "plt_active"}
	pfEmpty := &parsedFlags{vals: map[string]string{}}

	// platform falls back to active config
	v, err := resolveParam(cfg, pathParam{Name: "id", Role: "platform"}, pfEmpty)
	if err != nil || v != "plt_active" {
		t.Fatalf("platform fallback = %q, %v", v, err)
	}
	// --platform overrides
	v, _ = resolveParam(cfg, pathParam{Name: "id", Role: "platform"}, &parsedFlags{vals: map[string]string{"platform": "plt_x"}})
	if v != "plt_x" {
		t.Fatalf("platform override = %q", v)
	}
	// tenant requires --tenant
	if _, err := resolveParam(cfg, pathParam{Name: "id", Role: "tenant"}, pfEmpty); err == nil {
		t.Error("tenant without --tenant should error")
	}
	// explicit param requires its flag
	if _, err := resolveParam(cfg, pathParam{Name: "uid"}, pfEmpty); err == nil {
		t.Error("missing required --uid should error")
	}
	v, _ = resolveParam(cfg, pathParam{Name: "uid"}, &parsedFlags{vals: map[string]string{"uid": "u_1"}})
	if v != "u_1" {
		t.Fatalf("uid = %q", v)
	}
}

func TestBuildBody(t *testing.T) {
	withBody := command{HasBody: true}

	// --json passthrough
	b, err := buildBody(withBody, &parsedFlags{vals: map[string]string{"json": `{"a":1}`}})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := b.(map[string]any); m["a"].(float64) != 1 {
		t.Fatalf("json body = %#v", b)
	}

	// --field scalar inference + typed
	b, err = buildBody(withBody, &parsedFlags{
		vals:   map[string]string{},
		fields: []string{"name=acme", "count=5", "active=true", "meta:={\"k\":1}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := b.(map[string]any)
	if m["name"] != "acme" || m["count"].(float64) != 5 || m["active"] != true {
		t.Fatalf("field body = %#v", m)
	}
	if meta, ok := m["meta"].(map[string]any); !ok || meta["k"].(float64) != 1 {
		t.Fatalf("typed field = %#v", m["meta"])
	}

	// invalid --json
	if _, err := buildBody(withBody, &parsedFlags{vals: map[string]string{"json": "{bad"}}); err == nil {
		t.Error("invalid --json should error")
	}
}

func TestResolveCredential(t *testing.T) {
	t.Setenv("REALM_ID_ISSUER", "https://issuer.example")
	t.Setenv("REALM_ID_BFF", "https://bff.example")

	// Service mode: a static API key authenticates issuer-direct (ADR-062 §4).
	t.Setenv("REALM_ID_API_KEY", "rk_live_1")
	base, bearer := resolveCredential(&Config{SessionToken: "sess"})
	if base != "https://issuer.example" || bearer != "rk_live_1" {
		t.Fatalf("service mode: base=%q bearer=%q, want issuer + key", base, bearer)
	}

	// Session mode: the device-flow session token (rsid_) is a BFF credential,
	// so typed commands route through the BFF's /api/* admin passthrough — NOT
	// the issuer, which rejects it. Base must carry the /api prefix the BFF
	// strips before forwarding upstream.
	t.Setenv("REALM_ID_API_KEY", "")
	base, bearer = resolveCredential(&Config{SessionToken: "rsid_sess"})
	if base != "https://bff.example/api" || bearer != "rsid_sess" {
		t.Fatalf("session mode: base=%q bearer=%q, want bff/api + session", base, bearer)
	}
}
