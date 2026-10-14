package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testRealm = "0198aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee"

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// mintTestToken signs claims the way the issuer does (RS256, kid in the header).
func mintTestToken(t *testing.T, key *rsa.PrivateKey, kid, alg string, claims map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(map[string]string{"alg": alg, "typ": "JWT", "kid": kid})
	pb, _ := json.Marshal(claims)
	in := b64(hb) + "." + b64(pb)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + b64(sig)
}

func jwksFor(kid string, pub *rsa.PublicKey) map[string]any {
	return map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}}}
}

// jwksServer serves the realm JWKS at the path the issuer uses and counts hits.
func jwksServer(t *testing.T, kid string, pub *rsa.PublicKey) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/"+testRealm+"/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode(jwksFor(kid, pub))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

func testClaims(iss string) map[string]any {
	now := time.Now().Unix()
	return map[string]any{
		"iss": iss, "sub": "user-row-1", "aud": "realmid", "iat": now, "nbf": now,
		"exp": now + 900, "jti": "sess-1", "sid": "sess-1", "azp": "apikey:abc",
		"tenant_id": "tenant-1", "role": "admin", "scope": "orders:read orders:write",
		"partner_custom": "x",
	}
}

type vtResult struct {
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors"`
	Claims []struct {
		Name    string `json:"name"`
		Value   any    `json:"value"`
		Meaning string `json:"meaning"`
	} `json:"claims"`
}

func runVerify(t *testing.T, args ...string) (vtResult, string, int) {
	t.Helper()
	stdout, stderr, code := captureStd(t, func() int { return run(append([]string{"verify-token"}, args...)) })
	var r vtResult
	if strings.TrimSpace(stdout) != "" {
		if err := json.Unmarshal([]byte(stdout), &r); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
		}
	}
	return r, stderr, code
}

func TestVerifyToken_ValidToken(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv, _ := jwksServer(t, "k1", &key.PublicKey)
	tok := mintTestToken(t, key, "k1", "RS256", testClaims(srv.URL+"/"+testRealm))

	r, stderr, code := runVerify(t, "--issuer", srv.URL, "--output", "json", tok)
	if code != exitOK || !r.Valid {
		t.Fatalf("code=%d valid=%v errors=%v stderr=%s", code, r.Valid, r.Errors, stderr)
	}
	meanings := map[string]string{}
	for _, c := range r.Claims {
		meanings[c.Name] = c.Meaning
	}
	for _, name := range []string{"sub", "tenant_id", "aud", "azp", "scope", "sid", "jti", "role", "exp", "partner_custom"} {
		if strings.TrimSpace(meanings[name]) == "" {
			t.Errorf("claim %q has no meaning line (got %v)", name, meanings)
		}
	}
	if !strings.Contains(meanings["sub"], "membership") {
		t.Errorf("sub meaning must say it is a per-org membership id, got %q", meanings["sub"])
	}
}

func TestVerifyToken_Refusals(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv, hits := jwksServer(t, "k1", &key.PublicKey)
	iss := srv.URL + "/" + testRealm
	now := time.Now().Unix()

	expired := testClaims(iss)
	expired["exp"] = now - 60
	notYet := testClaims(iss)
	notYet["nbf"] = now + 3600
	notYet["exp"] = now + 7200

	cases := []struct {
		name string
		tok  string
		want string // substring of one error
		args []string
	}{
		{"bad signature", mintTestToken(t, other, "k1", "RS256", testClaims(iss)), "signature", nil},
		{"unknown kid", mintTestToken(t, key, "nope", "RS256", testClaims(iss)), "kid", nil},
		{"expired", mintTestToken(t, key, "k1", "RS256", expired), "expired", nil},
		{"not yet valid", mintTestToken(t, key, "k1", "RS256", notYet), "nbf", nil},
		{"wrong audience", mintTestToken(t, key, "k1", "RS256", testClaims(iss)), "aud", []string{"--aud", "someone-else"}},
		{"malformed", "not.a-jwt", "malformed", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := append([]string{"--issuer", srv.URL, "--output", "json"}, c.args...)
			r, _, code := runVerify(t, append(args, c.tok)...)
			if code == exitOK || r.Valid {
				t.Fatalf("must fail: code=%d valid=%v", code, r.Valid)
			}
			if !strings.Contains(strings.Join(r.Errors, "|"), c.want) {
				t.Fatalf("errors %v lack %q", r.Errors, c.want)
			}
		})
	}

	// Refusals that must happen BEFORE any JWKS fetch.
	before := atomic.LoadInt32(hits)
	for name, tok := range map[string]string{
		"HS256 alg":    mintTestToken(t, key, "k1", "HS256", testClaims(iss)),
		"foreign iss":  mintTestToken(t, key, "k1", "RS256", testClaims("https://evil.example/"+testRealm)),
		"alg none-ish": mintTestToken(t, key, "k1", "none", testClaims(iss)),
	} {
		r, _, code := runVerify(t, "--issuer", srv.URL, "--output", "json", tok)
		if code == exitOK || r.Valid {
			t.Errorf("%s must fail", name)
		}
	}
	if after := atomic.LoadInt32(hits); after != before {
		t.Errorf("JWKS was fetched %d times for tokens that must be refused up front (an iss outside the configured issuer must never steer the fetch)", after-before)
	}
}

func TestVerifyToken_Usage(t *testing.T) {
	if _, _, code := runVerify(t); code != exitUsage {
		t.Fatalf("no token should be a usage error, got %d", code)
	}
}
