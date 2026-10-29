package main

// Shared fixtures for the split-signing v3 CLI tests (unit E2, plan v3 §3.9 / §7.2 / W6; the v2
// W1.10 list; owner rulings 2026-10-29). Every test here drives the CLI through run() against a
// fake BFF over HTTP. The CLI generates from the vendored issuer spec and the re-vendor is
// tag-only, so nothing here depends on a re-vendored client: the BFF's `/api/*` passthrough is the
// only seam. Wire shapes marked [inferred] are the plan's silence, picked here so the builder has
// one concrete target; the builder should change a test only with the plan.

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type recReq struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// v3BFF records every request and answers through handle (which may be nil -> 200 {}).
type v3BFF struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []recReq
	handle func(w http.ResponseWriter, r recReq)
}

func (f *v3BFF) all() []recReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recReq(nil), f.reqs...)
}

// paths returns "METHOD path" for each recorded request, in order.
func (f *v3BFF) paths() []string {
	var out []string
	for _, r := range f.all() {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

// find returns the recorded requests whose "METHOD path" ends with suffix.
func (f *v3BFF) find(method, suffix string) []recReq {
	var out []recReq
	for _, r := range f.all() {
		if r.Method == method && strings.HasSuffix(r.Path, suffix) {
			out = append(out, r)
		}
	}
	return out
}

func (f *v3BFF) idx(method, suffix string) int {
	for i, r := range f.all() {
		if r.Method == method && strings.HasSuffix(r.Path, suffix) {
			return i
		}
	}
	return -1
}

func reply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// newBFF starts the fake, points the CLI at it, and writes a human device-flow session config.
// REALM_ID_API_KEY is cleared (session mode). The issuer URL is unroutable: a call to it fails.
func newBFF(t *testing.T, handle func(w http.ResponseWriter, r recReq)) *v3BFF {
	t.Helper()
	f := &v3BFF{t: t, handle: handle}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rr := recReq{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: b}
		f.mu.Lock()
		f.reqs = append(f.reqs, rr)
		f.mu.Unlock()
		if f.handle == nil {
			reply(w, 200, `{}`)
			return
		}
		f.handle(w, rr)
	}))
	t.Cleanup(f.srv.Close)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	cfg, _ := json.Marshal(map[string]string{"bff_url": f.srv.URL, "session_token": "rsid_human"})
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REALM_ID_CONFIG", cfgPath)
	t.Setenv("REALM_ID_BFF", f.srv.URL)
	t.Setenv("REALM_ID_ISSUER", "http://127.0.0.1:1") // must never be reached (BFF-only, R6/R63)
	t.Setenv("REALM_ID_API_KEY", "")
	t.Setenv("REALM_ID_SHARE_KEK", "")
	platformTokenCache = ""
	return f
}

func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return captureStd(t, func() int { return run(args) })
}

// ---- share envelope v1 (SPEC §14.5), implemented independently of the CLI ----

const v3Realm = "7c1d2f60-1a2b-4c3d-8e4f-5a6b7c8d9e0f"

func newKEK(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

// kekEnv is the REALM_ID_SHARE_KEK encoding: base64url, no padding, of the 32 raw bytes [inferred;
// the plan and SPEC §14.5 fix the KEK's length and id derivation but not its env encoding].
func kekEnv(k []byte) string { return base64.RawURLEncoding.EncodeToString(k) }

func openEnvelope(t *testing.T, raw []byte, kek []byte, realm string) shareBundleT {
	t.Helper()
	var env struct {
		V     int    `json:"v"`
		Alg   string `json:"alg"`
		KEKID string `json:"kek_id"`
		Nonce string `json:"nonce"`
		CT    string `json:"ct"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("share file is not an envelope: %v\n%s", err, raw)
	}
	sum := sha256.Sum256(append([]byte("realmid-kek-id/v1"), kek...))
	kid := hex.EncodeToString(sum[:])[:16]
	if env.V != 1 || env.Alg != "A256GCM" || env.KEKID != kid {
		t.Fatalf("envelope header = %+v, want v1/A256GCM/kek_id %s", env, kid)
	}
	nonce, _ := base64.RawURLEncoding.DecodeString(env.Nonce)
	ct, _ := base64.RawURLEncoding.DecodeString(env.CT)
	blk, _ := aes.NewCipher(kek)
	gcm, _ := cipher.NewGCM(blk)
	plain, err := gcm.Open(nil, nonce, ct, []byte("realmid-share/v1|"+realm+"|"+kid))
	if err != nil {
		t.Fatalf("envelope did not open under the KEK + realm AAD: %v", err)
	}
	var b shareBundleT
	if err := json.Unmarshal(plain, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

type shareEntryT struct {
	N        string `json:"n"`
	E        string `json:"e"`
	DPartner string `json:"d_partner"`
}

type shareBundleT struct {
	Realm  string                 `json:"realm"`
	Shares map[string]shareEntryT `json:"shares"`
}

// riBundleT is the RealmID-half bundle `signing-keys init` writes for `upload` [inferred layout].
type riBundleT struct {
	Realm string `json:"realm"`
	KID   string `json:"kid"`
	N     string `json:"n"`
	E     string `json:"e"`
	DRI   string `json:"d_ri,omitempty"`
}

func b64i(s string) *big.Int {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		panic("b64i: " + err.Error())
	}
	return new(big.Int).SetBytes(b)
}

// emsa is EMSA-PKCS1-v1_5 over a SHA-256 digest (the digest IS the hashed message, SPEC §14.6).
func emsa(digest []byte, k int) *big.Int {
	di := append([]byte{0x30, 0x31, 0x30, 0x0d, 0x06, 0x09, 0x60, 0x86, 0x48, 0x01, 0x65, 0x03, 0x04, 0x02, 0x01, 0x05, 0x00, 0x04, 0x20}, digest...)
	em := make([]byte, k)
	em[1] = 0x01
	for i := 2; i < k-len(di)-1; i++ {
		em[i] = 0xff
	}
	copy(em[k-len(di):], di)
	return new(big.Int).SetBytes(em)
}

func pubFrom(n, e string) *rsa.PublicKey {
	return &rsa.PublicKey{N: b64i(n), E: int(b64i(e).Int64())}
}

// keyMaterial is what one CLI `signing-keys init` produced, read back from disk.
type keyMaterial struct {
	KID, N, E, DRI, DPartner string
	BundlePath, ShareFile    string
	KEK                      []byte
}

// partialRI is RealmID's half-signature over digest: emsa(digest)^d_ri mod n, k bytes.
func (m keyMaterial) partialRI(digest []byte) string {
	n := b64i(m.N)
	k := (n.BitLen() + 7) / 8
	s := new(big.Int).Exp(emsa(digest, k), b64i(m.DRI), n)
	return base64.RawURLEncoding.EncodeToString(s.FillBytes(make([]byte, k)))
}

// runInit runs the real CLI `signing-keys init` offline and reads the result back.
func runInit(t *testing.T, kid string) keyMaterial {
	t.Helper()
	dir := t.TempDir()
	kek := newKEK(t)
	t.Setenv("REALM_ID_SHARE_KEK", kekEnv(kek))
	shareFile := filepath.Join(dir, "shares.json")
	_, stderr, code := runCLI(t, "signing-keys", "init", "--realm", v3Realm, "--kid", kid,
		"--share-to", "file:"+shareFile, "--out", dir)
	if code != exitOK {
		t.Fatalf("signing-keys init exit = %d, want 0\nstderr: %s", code, stderr)
	}
	raw, err := os.ReadFile(shareFile)
	if err != nil {
		t.Fatalf("init wrote no share file: %v", err)
	}
	sb := openEnvelope(t, raw, kek, v3Realm)
	ent, ok := sb.Shares[kid]
	if !ok {
		t.Fatalf("share bundle has no entry for kid %q: %+v", kid, sb)
	}
	bp := filepath.Join(dir, "bundle.json")
	braw, err := os.ReadFile(bp)
	if err != nil {
		t.Fatalf("init wrote no RealmID-half bundle at %s: %v", bp, err)
	}
	var rb riBundleT
	if err := json.Unmarshal(braw, &rb); err != nil || rb.DRI == "" {
		t.Fatalf("bundle.json unreadable or without d_ri: %v\n%s", err, braw)
	}
	return keyMaterial{KID: kid, N: rb.N, E: rb.E, DRI: rb.DRI, DPartner: ent.DPartner,
		BundlePath: bp, ShareFile: shareFile, KEK: kek}
}

func verifyDigestSig(m keyMaterial, digest []byte, sigB64 string) error {
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return err
	}
	return rsa.VerifyPKCS1v15(pubFrom(m.N, m.E), crypto.SHA256, digest, sig)
}

func jsonBody(t *testing.T, r recReq) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("request %s %s body is not JSON: %v\n%s", r.Method, r.Path, err, r.Body)
	}
	return m
}

func splitBase() string { return "/api/platforms/" + v3Realm + "/signing-keys/split" }
