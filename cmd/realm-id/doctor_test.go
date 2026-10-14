package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type doctorOut struct {
	Total   int `json:"total"`
	Ran     int `json:"ran"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
	Checks  []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	} `json:"checks"`
}

func (d doctorOut) status(name string) string {
	for _, c := range d.Checks {
		if c.Name == name {
			return c.Status
		}
	}
	return "<absent>"
}

// fakeIssuer is a healthy issuer for testRealm. Knobs let a test break one part.
type fakeIssuer struct {
	health     string // body status; default UP
	discIssuer string // discovery "issuer"; default <url>/<realm>
	emptyJWKS  bool
	keyOK      bool // accept the api key
	signWith   *rsa.PrivateKey
	jwksKey    *rsa.PrivateKey
}

func (f *fakeIssuer) serve(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var url string
	mux.HandleFunc("/.well-known/health", func(w http.ResponseWriter, r *http.Request) {
		st := f.health
		if st == "" {
			st = "UP"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "issuer", "status": st})
	})
	mux.HandleFunc("/"+testRealm+"/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		iss := f.discIssuer
		if iss == "" {
			iss = url + "/" + testRealm
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": iss, "jwks_uri": url + "/" + testRealm + "/.well-known/jwks.json"})
	})
	mux.HandleFunc("/"+testRealm+"/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		if f.emptyJWKS {
			_, _ = w.Write([]byte(`{"keys":[]}`))
			return
		}
		_ = json.NewEncoder(w).Encode(jwksFor("k1", &f.jwksKey.PublicKey))
	})
	mux.HandleFunc("/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !f.keyOK || body["grant_type"] != "platform_api_key" || body["api_key"] != "rk_live_good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized"}}`))
			return
		}
		c := testClaims(url + "/" + testRealm)
		c["exp"] = time.Now().Unix() + 900
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": mintTestToken(t, f.signWith, "k1", "RS256", c)})
	})
	srv := httptest.NewServer(mux)
	url = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

func runDoctor(t *testing.T, key string, args ...string) (doctorOut, int) {
	t.Helper()
	t.Setenv("REALM_ID_API_KEY", key)
	stdout, stderr, code := captureStd(t, func() int {
		return run(append([]string{"doctor", "--output", "json"}, args...))
	})
	var d doctorOut
	if err := json.Unmarshal([]byte(stdout), &d); err != nil {
		t.Fatalf("stdout not JSON (%v): %q stderr=%q", err, stdout, stderr)
	}
	return d, code
}

func newKey(t *testing.T) *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestDoctor_AllGreen(t *testing.T) {
	k := newKey(t)
	srv := (&fakeIssuer{keyOK: true, signWith: k, jwksKey: k}).serve(t)
	d, code := runDoctor(t, "rk_live_good", "--issuer", srv.URL, "--realm", testRealm)
	if code != exitOK || d.Failed != 0 || d.Ran != d.Total || d.Total < 5 || d.Passed != d.Ran {
		t.Fatalf("want all green: code=%d %+v", code, d)
	}
}

func TestDoctor_FailuresExitNonZero(t *testing.T) {
	k, other := newKey(t), newKey(t)
	cases := []struct {
		name  string
		f     *fakeIssuer
		key   string
		check string
	}{
		{"degraded health", &fakeIssuer{health: "DEGRADED", keyOK: true, signWith: k, jwksKey: k}, "rk_live_good", "issuer_health"},
		{"discovery names another issuer", &fakeIssuer{discIssuer: "https://elsewhere.example/x", keyOK: true, signWith: k, jwksKey: k}, "rk_live_good", "discovery"},
		{"empty jwks", &fakeIssuer{emptyJWKS: true, keyOK: true, signWith: k, jwksKey: k}, "rk_live_good", "jwks"},
		{"key refused", &fakeIssuer{keyOK: false, signWith: k, jwksKey: k}, "rk_live_bad", "api_key"},
		{"minted token not signed by published key", &fakeIssuer{keyOK: true, signWith: other, jwksKey: k}, "rk_live_good", "api_key_token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := c.f.serve(t)
			d, code := runDoctor(t, c.key, "--issuer", srv.URL, "--realm", testRealm)
			if code == exitOK || d.status(c.check) != "FAIL" || d.Failed < 1 {
				t.Fatalf("%s must FAIL and exit non-zero: code=%d status=%s %+v", c.check, code, d.status(c.check), d)
			}
		})
	}
}

func TestDoctor_UnreachableIssuer(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	d, code := runDoctor(t, "", "--issuer", url, "--realm", testRealm)
	if code == exitOK || d.status("issuer_health") != "FAIL" {
		t.Fatalf("unreachable issuer must FAIL: code=%d %+v", code, d)
	}
}

func TestDoctor_SkipsWhatItCannotCheck(t *testing.T) {
	k := newKey(t)
	srv := (&fakeIssuer{keyOK: true, signWith: k, jwksKey: k}).serve(t)
	// no realm, no api key: discovery/jwks/api checks SKIP, health still runs, exit 0.
	d, code := runDoctor(t, "", "--issuer", srv.URL)
	if code != exitOK || d.Skipped < 4 || d.Ran != 1 || d.status("issuer_health") != "PASS" {
		t.Fatalf("code=%d %+v", code, d)
	}
	for _, name := range []string{"discovery", "jwks", "api_key", "api_key_token"} {
		if d.status(name) != "SKIP" {
			t.Errorf("%s should SKIP, got %s", name, d.status(name))
		}
	}
	// the reason is on the line, not just the verdict
	for _, c := range d.Checks {
		if c.Status == "SKIP" && !strings.Contains(c.Detail, "--realm") && !strings.Contains(c.Detail, "REALM_ID_API_KEY") {
			t.Errorf("SKIP %s must say what to supply, got %q", c.Name, c.Detail)
		}
	}
}

func TestDoctor_TextStatesTheCount(t *testing.T) {
	k := newKey(t)
	srv := (&fakeIssuer{keyOK: true, signWith: k, jwksKey: k}).serve(t)
	t.Setenv("REALM_ID_API_KEY", "")
	stdout, _, _ := captureStd(t, func() int {
		return run([]string{"doctor", "--output", "text", "--issuer", srv.URL, "--realm", testRealm})
	})
	if !strings.Contains(stdout, "PASS") || !strings.Contains(stdout, "ran 3 of 5 checks") {
		t.Fatalf("text output must state the count:\n%s", stdout)
	}
}
