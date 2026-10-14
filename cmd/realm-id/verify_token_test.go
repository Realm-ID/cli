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
		// ADR-109 D3: jti is a fresh id per token; sid is the session id and is
		// stable across refreshes, so the two differ.
		"exp": now + 900, "jti": "jti-1", "sid": "sess-1", "azp": "apikey:abc",
		"tenant_id": "tenant-1", "role": "admin", "scope": "orders:read orders:write",
		"partner_custom": "x",
	}
}

type vtResult struct {
	Command    string        `json:"command"`
	OK         bool          `json:"ok"`
	Errors     []helperError `json:"errors"`
	NotChecked []string      `json:"not_checked"`
	Claims     []struct {
		Name    string `json:"name"`
		Value   any    `json:"value"`
		Meaning string `json:"meaning"`
	} `json:"claims"`
}

func (r vtResult) hasCode(code string) bool {
	for _, e := range r.Errors {
		if e.Code == code {
			return true
		}
	}
	return false
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
	if code != exitOK || !r.OK || r.Command != "verify-token" {
		t.Fatalf("code=%d ok=%v errors=%v stderr=%s", code, r.OK, r.Errors, stderr)
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
	if !strings.Contains(meanings["role"], "lookup") {
		t.Errorf("role is the lookup key for the permission set on the direct-bearer path, got %q", meanings["role"])
	}
	// What the CLI did NOT check is part of the answer.
	joined := strings.Join(r.NotChecked, "|")
	if !strings.Contains(joined, "aud") || !strings.Contains(joined, "revocation") {
		t.Errorf("not_checked must name aud (no --aud given) and revocation, got %v", r.NotChecked)
	}
}

func TestVerifyToken_NotCheckedDropsAudWhenGiven(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv, _ := jwksServer(t, "k1", &key.PublicKey)
	tok := mintTestToken(t, key, "k1", "RS256", testClaims(srv.URL+"/"+testRealm))
	r, _, _ := runVerify(t, "--issuer", srv.URL, "--output", "json", "--aud", "realmid", tok)
	if !r.OK || strings.Contains(strings.Join(r.NotChecked, "|"), "aud") || !strings.Contains(strings.Join(r.NotChecked, "|"), "revocation") {
		t.Fatalf("ok=%v not_checked=%v", r.OK, r.NotChecked)
	}
	// the text output states it too
	out, _, _ := captureStd(t, func() int { return run([]string{"verify-token", "--issuer", srv.URL, "--output", "text", tok}) })
	if !strings.Contains(out, "not checked") || !strings.Contains(out, "revocation") {
		t.Errorf("text output must list what was not checked:\n%s", out)
	}
}

func TestVerifyToken_Refusals(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv, _ := jwksServer(t, "k1", &key.PublicKey)
	iss := srv.URL + "/" + testRealm
	now := time.Now().Unix()

	expired := testClaims(iss)
	expired["exp"] = now - 60
	notYet := testClaims(iss)
	notYet["nbf"] = now + 3600
	notYet["exp"] = now + 7200
	withEvents := testClaims(iss)
	withEvents["events"] = map[string]any{"x": 1}
	// Shaped like the issuer's internal MFA challenge token (authsvc
	// mintMFAChallenge): sub "authsvc", scope "auth:mfa:challenge". It is signed
	// by the same key, so the signature alone would pass it as a real token.
	mfaShaped := testClaims(iss)
	mfaShaped["sub"], mfaShaped["scope"] = "authsvc", "auth:mfa:challenge"

	cases := []struct {
		name string
		tok  string
		want string // error code
		args []string
	}{
		{"bad signature", mintTestToken(t, other, "k1", "RS256", testClaims(iss)), "bad_signature", nil},
		{"unknown kid", mintTestToken(t, key, "nope", "RS256", testClaims(iss)), "unknown_kid", nil},
		{"expired", mintTestToken(t, key, "k1", "RS256", expired), "expired", nil},
		{"not yet valid", mintTestToken(t, key, "k1", "RS256", notYet), "not_yet_valid", nil},
		{"wrong audience", mintTestToken(t, key, "k1", "RS256", testClaims(iss)), "wrong_audience", []string{"--aud", "someone-else"}},
		{"malformed", "not.a-jwt", "malformed", nil},
		{"events claim", mintTestToken(t, key, "k1", "RS256", withEvents), "not_an_access_token", nil},
		{"mfa challenge shaped", mintTestToken(t, key, "k1", "RS256", mfaShaped), "not_an_access_token", nil},
		{"non-access typ", mintTypToken(t, key, "k1", "secevent+jwt", testClaims(iss)), "not_an_access_token", nil},
		{"no typ", mintTypToken(t, key, "k1", "", testClaims(iss)), "not_an_access_token", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := append([]string{"--issuer", srv.URL, "--output", "json"}, c.args...)
			r, _, code := runVerify(t, append(args, c.tok)...)
			if code == exitOK || r.OK {
				t.Fatalf("must fail: code=%d ok=%v", code, r.OK)
			}
			if !r.hasCode(c.want) {
				t.Fatalf("errors %v lack code %q", r.Errors, c.want)
			}
		})
	}
}

// mintTypToken is mintTestToken with a chosen `typ` header ("" omits it).
func mintTypToken(t *testing.T, key *rsa.PrivateKey, kid, typ string, claims map[string]any) string {
	t.Helper()
	h := map[string]string{"alg": "RS256", "kid": kid}
	if typ != "" {
		h["typ"] = typ
	}
	hb, _ := json.Marshal(h)
	pb, _ := json.Marshal(claims)
	in := b64(hb) + "." + b64(pb)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + b64(sig)
}

// TestVerifyToken_NeverFetchesFromTheTokensIss: the foreign host DOES serve the
// signing key, so a verifier that followed `iss` would pass the forged token.
func TestVerifyToken_NeverFetchesFromTheTokensIss(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	good, goodHits := jwksServer(t, "k1", &key.PublicKey)
	evil, evilHits := jwksServer(t, "k1", &key.PublicKey)

	cases := []struct {
		name, tok, code string
	}{
		{"foreign iss", mintTestToken(t, key, "k1", "RS256", testClaims(evil.URL+"/"+testRealm)), "wrong_issuer"},
		{"HS256 alg", mintTestToken(t, key, "k1", "HS256", testClaims(good.URL+"/"+testRealm)), "wrong_algorithm"},
		{"alg none", mintTestToken(t, key, "k1", "none", testClaims(good.URL+"/"+testRealm)), "wrong_algorithm"},
	}
	for _, c := range cases {
		r, _, code := runVerify(t, "--issuer", good.URL, "--output", "json", c.tok)
		if code == exitOK || r.OK || !r.hasCode(c.code) {
			t.Errorf("%s: want refusal with code %s, got code=%d ok=%v errors=%v", c.name, c.code, code, r.OK, r.Errors)
		}
	}
	if n := atomic.LoadInt32(evilHits); n != 0 {
		t.Errorf("the token's own iss host was contacted %d times", n)
	}
	if n := atomic.LoadInt32(goodHits); n != 0 {
		t.Errorf("the configured issuer's JWKS was fetched %d times for tokens refused up front", n)
	}
}

func TestVerifyToken_Usage(t *testing.T) {
	if _, _, code := runVerify(t); code != exitUsage {
		t.Fatalf("no token should be a usage error, got %d", code)
	}
}
