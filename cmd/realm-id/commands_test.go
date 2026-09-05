package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestResolveCredential_ServiceModeExchangesTheKey pins the fix for a bug the
// PREVIOUS version of this test actively protected: it asserted
// `bearer == "rk_live_1"`, i.e. that the raw api key is sent as the bearer.
// That is what the CLI did, and the issuer has never accepted it — requireAuth
// runs the bearer through LocalVerifier.Verify, which rejects anything that is
// not a 3-part JWT. The test encoded the implementation instead of the
// contract, so it passed for the whole time service mode was broken. Asserting
// the EXCHANGE, against a server that behaves like the issuer, is what makes it
// a test of the contract.
func TestResolveCredential_ServiceModeExchangesTheKey(t *testing.T) {
	platformTokenCache = ""
	t.Cleanup(func() { platformTokenCache = "" })

	var gotPath, gotGrant, gotKey, gotAuthz string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotPath = r.URL.Path
		gotAuthz = r.Header.Get("Authorization")
		var in struct {
			GrantType string `json:"grant_type"`
			APIKey    string `json:"api_key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		gotGrant, gotKey = in.GrantType, in.APIKey
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"eyJ.header.sig","expires_in":600}`))
	}))
	defer srv.Close()

	t.Setenv("REALM_ID_ISSUER", srv.URL)
	t.Setenv("REALM_ID_BFF", "https://bff.example")
	t.Setenv("REALM_ID_API_KEY", "rk_live_1")

	base, bearer, err := resolveCredential(&Config{SessionToken: "sess"})
	if err != nil {
		t.Fatalf("service mode: unexpected error: %v", err)
	}
	if base != srv.URL {
		t.Errorf("base = %q, want the issuer %q", base, srv.URL)
	}
	if bearer == "rk_live_1" {
		t.Fatal("service mode still sends the RAW api key as the bearer — the issuer answers 401 invalid bearer")
	}
	if bearer != "eyJ.header.sig" {
		t.Errorf("bearer = %q, want the exchanged platform JWT", bearer)
	}
	if gotPath != "/auth/login" || gotGrant != "platform_api_key" || gotKey != "rk_live_1" {
		t.Errorf("exchange sent path=%q grant=%q key=%q; want POST /auth/login platform_api_key + the raw key",
			gotPath, gotGrant, gotKey)
	}
	// The key travels in the BODY of the bootstrap call, never as a bearer —
	// that is the whole point of the two-endpoint surface (ADR-051).
	if gotAuthz != "" {
		t.Errorf("exchange sent Authorization=%q; the bootstrap call must be unauthenticated", gotAuthz)
	}

	// Cached for the process: one command must not re-exchange.
	if _, _, err := resolveCredential(&Config{SessionToken: "sess"}); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if calls != 1 {
		t.Errorf("exchange ran %d times, want 1 (cached for the process)", calls)
	}
}

func TestResolveCredential_ExchangeFailureIsReported(t *testing.T) {
	platformTokenCache = ""
	t.Cleanup(func() { platformTokenCache = "" })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"invalid_api_key","message":"nope"}`))
	}))
	defer srv.Close()

	t.Setenv("REALM_ID_ISSUER", srv.URL)
	t.Setenv("REALM_ID_API_KEY", "rk_live_bad")

	_, _, err := resolveCredential(&Config{})
	if err == nil {
		t.Fatal("a rejected api key must surface an error, not an empty bearer")
	}
	// The issuer's own body must reach the user: "401" alone leaves them unable
	// to tell a bad key from a bad URL.
	if !strings.Contains(err.Error(), "invalid_api_key") {
		t.Errorf("error %q does not carry the issuer's response body", err)
	}
}

func TestResolveCredential_SessionMode(t *testing.T) {
	platformTokenCache = ""
	t.Cleanup(func() { platformTokenCache = "" })
	t.Setenv("REALM_ID_ISSUER", "https://issuer.example")
	t.Setenv("REALM_ID_BFF", "https://bff.example")

	// The device-flow session token (rsid_) is a BFF credential, so typed
	// commands route through the BFF's /api/* admin passthrough — NOT the
	// issuer, which rejects it. Base must carry the /api prefix the BFF strips
	// before forwarding upstream. No exchange happens on this path.
	t.Setenv("REALM_ID_API_KEY", "")
	base, bearer, err := resolveCredential(&Config{SessionToken: "rsid_sess"})
	if err != nil {
		t.Fatalf("session mode: unexpected error: %v", err)
	}
	if base != "https://bff.example/api" || bearer != "rsid_sess" {
		t.Fatalf("session mode: base=%q bearer=%q, want bff/api + session", base, bearer)
	}
}

// TestQueryParamLabel pins the read/write split behind the help annotation on
// a query flag. A GET only ever narrows or pages what it returns, so its
// query params are honestly "(filter)"; a query param on any other method is
// steering what the write DOES — e.g. `?override_seated=true` on the
// role-templates PATCH forces through an edit the issuer would otherwise
// refuse with `409 role_template_seated` — and must never be labelled a
// filter.
func TestQueryParamLabel(t *testing.T) {
	cases := []struct {
		method string
		want   string
	}{
		{"GET", "(filter)"},
		{"PATCH", "(option — changes what this call does, not what it returns)"},
		{"POST", "(option — changes what this call does, not what it returns)"},
		{"PUT", "(option — changes what this call does, not what it returns)"},
		{"DELETE", "(option — changes what this call does, not what it returns)"},
	}
	for _, c := range cases {
		if got := queryParamLabel(c.method); got != c.want {
			t.Errorf("queryParamLabel(%q) = %q, want %q", c.method, got, c.want)
		}
	}
}

// TestPrintCommandHelp_OverrideSeatedIsNotAFilter is the exact regression this
// fix exists for: `role-templates update --help` must not describe
// `override_seated` — a safety-guard override, not a narrowing parameter — as
// a "(filter)".
func TestPrintCommandHelp_OverrideSeatedIsNotAFilter(t *testing.T) {
	cmd := command{
		Group:   []string{"role-templates"},
		Verb:    "update",
		Method:  "PATCH",
		Path:    "/platforms/{id}/role-templates/{templateId}",
		Query:   []queryParam{{Name: "override_seated"}},
		HasBody: true,
	}
	var buf strings.Builder
	printCommandHelp(&buf, cmd)
	out := buf.String()
	if strings.Contains(out, "override_seated <val> (filter)") {
		t.Fatalf("override_seated is a safety-guard override, not a filter; got:\n%s", out)
	}
	if !strings.Contains(out, "override_seated <val> (option") {
		t.Fatalf("expected override_seated labelled as an option, got:\n%s", out)
	}
}
