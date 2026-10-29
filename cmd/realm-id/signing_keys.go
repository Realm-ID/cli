package main

// `realm-id signing-keys …` — split signing custody (plan v3 §3.9/§7.2; v2 §5.1 + W1.10; R5, R9,
// R23, R36, R38, R39, R73, R92; owner rulings 2026-10-29 C5#1 lease ticket, C5#4 downgrade
// challenge, C5#6 JWKS pin). Everything but `init` goes through the BFF `/api/*` passthrough under
// a PERSON's session (status/pin are reads and work in any session); the CLI never calls the
// issuer. `init` is offline: it deals the key, writes RealmID's half to a bundle for `upload`,
// and puts the partner half in the named share store.
//
// The share envelope v1, the dealer split and the cosignature maths are implemented here on the
// standard library, independently of sdk/go (SPEC §14.5-14.6): the CLI must build without the
// sdk module beside it, and the E2 tests check the result with their own implementation.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var rawB64 = base64.RawURLEncoding

func cmdSigningKeys(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: realm-id signing-keys <init|upload|status|pin|extend|cancel|rotate|recover|downgrade> --realm <id> …")
		return exitUsage
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "init", "upload", "status", "pin", "extend", "cancel", "rotate", "recover", "downgrade":
	default:
		fmt.Fprintf(os.Stderr, "unknown signing-keys verb %q\n", verb)
		return exitUsage
	}
	f, err := parseVerbFlags(rest,
		[]string{"realm", "kid", "share-to", "out", "bundle", "overlap", "by", "cosign-from", "totp"}, []string{"cancel"})
	if err != nil {
		return failCode(err, exitUsage)
	}
	if verb == "init" {
		return skInit(f)
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	if verb != "status" && verb != "pin" {
		if code := needsHuman(cfg, "signing-keys "+verb, false); code != 0 {
			return code
		}
	}
	realm := f.vals["realm"]
	if realm == "" {
		realm = cfg.Platform
	}
	if realm == "" {
		return failCode(errors.New("no realm: pass --realm <id> or `realm-id config set platform <id>`"), exitUsage)
	}
	base := "/platforms/" + url.PathEscape(realm) + "/signing-keys/split"
	switch verb {
	case "status":
		st, raw, err := bffCall(cfg, http.MethodGet, base, nil)
		if err != nil {
			return callExit(err)
		}
		return skPrint(st, raw)
	case "pin":
		return skPin(cfg, base)
	case "upload":
		return skUpload(cfg, f, realm, base)
	case "extend":
		if f.vals["by"] == "" {
			return failCode(errors.New("usage: realm-id signing-keys extend --realm <id> --by <duration, e.g. 24h>"), exitUsage)
		}
		return skMutate(cfg, f, http.MethodPost, base+"/overlap/extend", map[string]any{"by": f.vals["by"]})
	case "cancel":
		return skMutate(cfg, f, http.MethodPost, base+"/overlap/cancel", nil)
	case "rotate":
		return skRotate(cfg, f, realm, base, "rotation")
	case "recover":
		return skRotate(cfg, f, realm, base, "recovery")
	default: // downgrade
		return skDowngrade(cfg, f, realm, base)
	}
}

// skExit maps a response: the split-signing codes are exit 9 (not the generic 401 -> 6).
func skExit(status int, raw []byte) int {
	if status/100 != 2 {
		switch errorCode(raw) {
		case "handshake_failed", "share_load_failed", "cosig_invalid":
			return exitSplitSigning
		}
	}
	return exitForStatus(status)
}

func skPrint(status int, raw []byte) int {
	printBody(status, raw)
	return skExit(status, raw)
}

func skMutate(cfg *Config, f *verbFlags, method, path string, body any) int {
	st, raw, abort := stepUpCall(cfg, method, path, body, f.vals["totp"])
	if abort >= 0 {
		return abort
	}
	return skPrint(st, raw)
}

// ---- share material ----

type shareEntry struct {
	N        string `json:"n"`
	E        string `json:"e"`
	DPartner string `json:"d_partner"`
}

type shareBundle struct {
	Realm    string                `json:"realm"`
	Shares   map[string]shareEntry `json:"shares"`
	Previous json.RawMessage       `json:"previous,omitempty"`
}

// kekFromEnv reads REALM_ID_SHARE_KEK: base64url, no padding, 32 bytes.
func kekFromEnv() ([]byte, error) {
	v := strings.TrimSpace(envOr("REALM_ID_SHARE_KEK", ""))
	if v == "" {
		return nil, errors.New("REALM_ID_SHARE_KEK is not set (32 bytes, base64url without padding)")
	}
	k, err := rawB64.DecodeString(v)
	if err != nil || len(k) != 32 {
		return nil, errors.New("REALM_ID_SHARE_KEK must be 32 bytes, base64url without padding")
	}
	return k, nil
}

func kekID(kek []byte) string {
	sum := sha256.Sum256(append([]byte("realmid-kek-id/v1"), kek...))
	return hex.EncodeToString(sum[:])[:16]
}

func envelopeGCM(kek []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// fileShareStore is envelope v1 (SPEC §14.5): AES-256-GCM, AAD realmid-share/v1|realm|kek id.
type fileShareStore struct {
	path, realm string
	kek         []byte
}

func (s fileShareStore) aad() []byte {
	return []byte("realmid-share/v1|" + s.realm + "|" + kekID(s.kek))
}

func (s fileShareStore) load() (*shareBundle, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("share store %s: %w", s.path, err)
	}
	var env struct {
		V     int    `json:"v"`
		Alg   string `json:"alg"`
		KEKID string `json:"kek_id"`
		Nonce string `json:"nonce"`
		CT    string `json:"ct"`
	}
	if json.Unmarshal(raw, &env) != nil || env.V != 1 || env.Alg != "A256GCM" {
		return nil, fmt.Errorf("share store %s is not an encrypted share bundle", s.path)
	}
	if env.KEKID != kekID(s.kek) {
		return nil, fmt.Errorf("share store %s is sealed under KEK %s, not the configured %s", s.path, env.KEKID, kekID(s.kek))
	}
	gcm, err := envelopeGCM(s.kek)
	if err != nil {
		return nil, err
	}
	nonce, e1 := rawB64.DecodeString(env.Nonce)
	ct, e2 := rawB64.DecodeString(env.CT)
	if e1 != nil || e2 != nil || len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("share store %s: malformed envelope", s.path)
	}
	plain, err := gcm.Open(nil, nonce, ct, s.aad())
	if err != nil {
		return nil, fmt.Errorf("share store %s did not decrypt (wrong KEK or realm?)", s.path)
	}
	var b shareBundle
	if json.Unmarshal(plain, &b) != nil {
		return nil, fmt.Errorf("share store %s: bundle is not valid JSON", s.path)
	}
	if b.Shares == nil {
		b.Shares = map[string]shareEntry{}
	}
	return &b, nil
}

// store writes the sealed bundle atomically, mode 0600.
func (s fileShareStore) store(b *shareBundle) error {
	gcm, err := envelopeGCM(s.kek)
	if err != nil {
		return err
	}
	plain, _ := json.Marshal(b)
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	out, _ := json.Marshal(map[string]any{"v": 1, "alg": "A256GCM", "kek_id": kekID(s.kek),
		"nonce": rawB64.EncodeToString(nonce), "ct": rawB64.EncodeToString(gcm.Seal(nil, nonce, plain, s.aad()))})
	return writeFileAtomic(s.path, out)
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".realmid-*")
	if err != nil {
		return err
	}
	fail := func(e error) error { _ = tmp.Close(); _ = os.Remove(tmp.Name()); return e }
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// shareTarget is where a new partner half goes: a sealed file, or (explicitly) stdout.
type shareTarget struct {
	file   *fileShareStore
	stdout bool
}

// parseShareTarget resolves --share-to. There is no default and no silent stdout (R9); a file
// store needs the KEK (SPEC §14.5); redis/gsm stores are SDK add-ons the CLI does not carry.
func parseShareTarget(spec, realm string) (*shareTarget, error) {
	switch {
	case spec == "":
		return nil, usageCredError{"--share-to is required: file:PATH (sealed under REALM_ID_SHARE_KEK) or stdout; there is no default"}
	case spec == "stdout":
		return &shareTarget{stdout: true}, nil
	case strings.HasPrefix(spec, "file:"):
		kek, err := kekFromEnv()
		if err != nil {
			return nil, usageCredError{"--share-to file: needs the KEK: " + err.Error()}
		}
		return &shareTarget{file: &fileShareStore{path: strings.TrimPrefix(spec, "file:"), realm: realm, kek: kek}}, nil
	case strings.HasPrefix(spec, "redis:"), strings.HasPrefix(spec, "gsm:"):
		if _, err := kekFromEnv(); err != nil {
			return nil, usageCredError{"--share-to " + strings.SplitN(spec, ":", 2)[0] + ": needs the KEK: " + err.Error()}
		}
		return nil, usageCredError{"--share-to " + strings.SplitN(spec, ":", 2)[0] + ": is not carried by this CLI build; write to file:PATH and move the sealed file"}
	}
	return nil, usageCredError{fmt.Sprintf("--share-to %q: want file:PATH or stdout", spec)}
}

// ---- RSA dealer split and cosignature maths (SPEC §14.6) ----

var sha256DigestInfo = []byte{0x30, 0x31, 0x30, 0x0d, 0x06, 0x09, 0x60, 0x86, 0x48, 0x01, 0x65, 0x03, 0x04, 0x02, 0x01, 0x05, 0x00, 0x04, 0x20}

// emsaInt is EMSA-PKCS1-v1_5 over a SHA-256 digest for a k-byte modulus.
func emsaInt(digest []byte, k int) (*big.Int, error) {
	t := append(append([]byte{}, sha256DigestInfo...), digest...)
	if len(digest) != sha256.Size || k < len(t)+11 {
		return nil, errors.New("modulus too short for the signature encoding")
	}
	em := make([]byte, k)
	em[1] = 0x01
	for i := 2; i < k-len(t)-1; i++ {
		em[i] = 0xff
	}
	copy(em[k-len(t):], t)
	return new(big.Int).SetBytes(em), nil
}

type dealt struct {
	n, e, dRI, dPartner *big.Int
}

// dealKey generates a 2048-bit RSA key and splits its private exponent d = d_ri + d_partner
// (mod phi) at random, so neither half alone signs (R5).
func dealKey() (*dealt, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generating the key: %w", err)
	}
	phi := new(big.Int).Mul(new(big.Int).Sub(key.Primes[0], big.NewInt(1)), new(big.Int).Sub(key.Primes[1], big.NewInt(1)))
	for {
		dRI, err := rand.Int(rand.Reader, phi)
		if err != nil {
			return nil, err
		}
		dP := new(big.Int).Sub(key.D, dRI)
		dP.Mod(dP, phi)
		if dRI.Sign() > 0 && dP.Sign() > 0 {
			return &dealt{n: key.N, e: big.NewInt(int64(key.E)), dRI: dRI, dPartner: dP}, nil
		}
	}
}

func bigB64(x *big.Int) string { return rawB64.EncodeToString(x.Bytes()) }

func fromB64(s string) (*big.Int, error) {
	b, err := rawB64.DecodeString(s)
	if err != nil || len(b) == 0 {
		return nil, errors.New("not base64url")
	}
	return new(big.Int).SetBytes(b), nil
}

func fingerprint(n, e *big.Int) string {
	sum := sha256.Sum256(append(n.Bytes(), e.Bytes()...))
	return hex.EncodeToString(sum[:])
}

// completeDigest multiplies RealmID's partial by the partner partial over digest and verifies
// s^e == m before releasing it (verify-before-release).
func completeDigest(ent shareEntry, digest, partialRI []byte) ([]byte, error) {
	n, e1 := fromB64(ent.N)
	e, e2 := fromB64(ent.E)
	dP, e3 := fromB64(ent.DPartner)
	if e1 != nil || e2 != nil || e3 != nil {
		return nil, errors.New("the share entry is malformed")
	}
	k := (n.BitLen() + 7) / 8
	m, err := emsaInt(digest, k)
	if err != nil {
		return nil, err
	}
	sRI := new(big.Int).SetBytes(partialRI)
	if len(partialRI) == 0 || sRI.Sign() == 0 || sRI.Cmp(n) >= 0 {
		return nil, errors.New("RealmID's partial signature is out of range")
	}
	s := new(big.Int).Mul(sRI, ladderExp(m, dP, n))
	s.Mod(s, n)
	if new(big.Int).Exp(s, e, n).Cmp(m) != 0 {
		return nil, errors.New("the finished signature does not verify under the share's public key; refusing to send it")
	}
	return s.FillBytes(make([]byte, k)), nil
}

// ladderExp is a Montgomery ladder over a fixed bit count (the modulus length), so the operation
// sequence does not depend on the exponent's bits (R27). math/big is not constant-time.
func ladderExp(base, exp, n *big.Int) *big.Int {
	r0, r1 := big.NewInt(1), new(big.Int).Set(base)
	for i := n.BitLen() - 1; i >= 0; i-- {
		pair := [2]*big.Int{r0, r1}
		b := exp.Bit(i)
		prod := new(big.Int).Mul(pair[0], pair[1])
		prod.Mod(prod, n)
		sq := new(big.Int).Mul(pair[b], pair[b])
		sq.Mod(sq, n)
		pair[1-b] = prod
		pair[b] = sq
		r0, r1 = pair[0], pair[1]
	}
	return r0
}

// ---- init ----

func newKID() string { return "k-" + time.Now().UTC().Format("20060102-150405") }

func skInit(f *verbFlags) int {
	realm, kid := f.vals["realm"], f.vals["kid"]
	if realm == "" || kid == "" {
		return failCode(errors.New("usage: realm-id signing-keys init --realm <id> --kid <kid> --share-to <file:PATH|stdout> [--out DIR]"), exitUsage)
	}
	tgt, err := parseShareTarget(f.vals["share-to"], realm)
	if err != nil {
		return failCode(err, exitUsage)
	}
	out := f.vals["out"]
	if out == "" {
		out = "."
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		return fail(err)
	}
	d, err := dealKey()
	if err != nil {
		return fail(err)
	}
	entry := shareEntry{N: bigB64(d.n), E: bigB64(d.e), DPartner: bigB64(d.dPartner)}
	// The partner half is stored BEFORE anything else is written: losing it loses the realm.
	shared, code := putShare(tgt, realm, kid, entry)
	if code != 0 {
		return code
	}
	bundle, _ := json.Marshal(map[string]string{"realm": realm, "kid": kid, "n": entry.N, "e": entry.E, "d_ri": bigB64(d.dRI)})
	bundlePath, pubPath := filepath.Join(out, "bundle.json"), filepath.Join(out, "public.json")
	if err := writeFileAtomic(bundlePath, bundle); err != nil {
		return fail(fmt.Errorf("writing %s: %w", bundlePath, err))
	}
	pub, _ := json.MarshalIndent(map[string]any{"keys": []any{pubJWK(kid, entry.N, entry.E)}}, "", "  ")
	if err := writeFileAtomic(pubPath, pub); err != nil {
		return fail(fmt.Errorf("writing %s: %w", pubPath, err))
	}
	res := map[string]any{"kid": kid, "fingerprint": fingerprint(d.n, d.e), "n_bits": d.n.BitLen(),
		"written_to": map[string]string{"bundle": bundlePath, "public": pubPath, "share": shared.where}}
	if tgt.stdout {
		fmt.Fprintln(os.Stdout, "::add-mask::"+entry.DPartner)
		res["share"] = shared.bundle
	}
	writeJSON(res)
	return exitOK
}

func pubJWK(kid, n, e string) map[string]string {
	return map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid, "n": n, "e": e}
}

type putResult struct {
	where  string
	bundle *shareBundle
}

// putShare adds entry under kid to the target (a sealed file keeps its other kids). stdout
// targets are NOT printed here; the caller does that, after masking.
func putShare(tgt *shareTarget, realm, kid string, entry shareEntry) (putResult, int) {
	b := &shareBundle{Realm: realm, Shares: map[string]shareEntry{}}
	if tgt.file != nil {
		if _, err := os.Stat(tgt.file.path); err == nil {
			old, lerr := tgt.file.load()
			if lerr != nil {
				return putResult{}, failCode(lerr, exitSplitSigning)
			}
			if old.Realm != realm {
				return putResult{}, failCode(fmt.Errorf("share store %s belongs to realm %s, not %s", tgt.file.path, old.Realm, realm), exitUsage)
			}
			b = old
		}
	}
	b.Shares[kid] = entry
	if tgt.file == nil {
		return putResult{where: "stdout", bundle: b}, 0
	}
	if err := tgt.file.store(b); err != nil {
		return putResult{}, failCode(fmt.Errorf("writing the share store: %w", err), exitErr)
	}
	return putResult{where: tgt.file.path, bundle: b}, 0
}

// ---- upload ----

func skUpload(cfg *Config, f *verbFlags, realm, base string) int {
	if f.vals["bundle"] == "" {
		return failCode(errors.New("usage: realm-id signing-keys upload --realm <id> --bundle <bundle.json> [--overlap 24h]"), exitUsage)
	}
	raw, err := os.ReadFile(f.vals["bundle"])
	if err != nil {
		return failCode(fmt.Errorf("reading the bundle: %w", err), exitUsage)
	}
	var b map[string]string
	if json.Unmarshal(raw, &b) != nil || b["d_ri"] == "" || b["kid"] == "" {
		return failCode(errors.New("the bundle has no RealmID half (d_ri): already uploaded, or not a bundle from `signing-keys init`"), exitUsage)
	}
	if b["realm"] != "" && b["realm"] != realm {
		return failCode(fmt.Errorf("the bundle is for realm %s, not %s", b["realm"], realm), exitUsage)
	}
	body := map[string]any{"kid": b["kid"], "n": b["n"], "e": b["e"], "d_ri": b["d_ri"]}
	if o := f.vals["overlap"]; o != "" {
		body["overlap_d"] = o
	}
	st, resp, abort := stepUpCall(cfg, http.MethodPost, base, body, f.vals["totp"])
	if abort >= 0 {
		return abort
	}
	if st/100 == 2 { // RealmID holds its half now: drop the local copy (the only other one)
		kept, _ := json.Marshal(map[string]string{"realm": realm, "kid": b["kid"], "n": b["n"], "e": b["e"]})
		if err := writeFileAtomic(f.vals["bundle"], kept); err != nil {
			fmt.Fprintf(os.Stderr, "warning: uploaded, but could not drop d_ri from %s: %v (delete it by hand)\n", f.vals["bundle"], err)
		}
	}
	return skPrint(st, resp)
}

// ---- pin ----

// skPin prints the public-only standard JWKS for the base-trust secret (C5#6).
func skPin(cfg *Config, base string) int {
	var out struct {
		KID string `json:"kid"`
		Pub struct {
			N string `json:"n"`
			E string `json:"e"`
		} `json:"public_key"`
	}
	st, raw, err := bffCall(cfg, http.MethodGet, base, nil)
	if err != nil {
		return callExit(err)
	}
	if st/100 != 2 {
		return skPrint(st, raw)
	}
	decodeBody(raw, &out)
	if out.KID == "" || out.Pub.N == "" {
		return failCode(errors.New("the realm has no split key to pin"), exitNotFound)
	}
	writeJSON(map[string]any{"keys": []any{pubJWK(out.KID, out.Pub.N, out.Pub.E)}})
	return exitOK
}

// ---- rotate / recover ----

// skRotate takes the rotation lease ITSELF (reason rotation|recovery, R92), deals a new key,
// stores the partner half, and uploads with lease_id (C5#1). Recovery has no old share to
// merge, so it defaults to printing the new bundle masked.
func skRotate(cfg *Config, f *verbFlags, realm, base, reason string) int {
	kid := f.vals["kid"]
	if kid == "" {
		kid = newKID()
	}
	spec := f.vals["share-to"]
	if spec == "" && reason == "recovery" {
		spec = "stdout"
	}
	tgt, err := parseShareTarget(spec, realm)
	if err != nil {
		return failCode(err, exitUsage)
	}
	d, err := dealKey()
	if err != nil {
		return fail(err)
	}
	st, raw, abort := stepUpCall(cfg, http.MethodPost, base+"/rotation-lease", map[string]string{"reason": reason}, f.vals["totp"])
	if abort >= 0 {
		return abort
	}
	if st/100 != 2 {
		return skPrint(st, raw)
	}
	var lease struct {
		LeaseID string `json:"lease_id"`
	}
	decodeBody(raw, &lease)
	if lease.LeaseID == "" {
		return failCode(errors.New("the rotation lease came back without a lease_id"), exitErr)
	}
	entry := shareEntry{N: bigB64(d.n), E: bigB64(d.e), DPartner: bigB64(d.dPartner)}
	// Stored BEFORE the upload: an uploaded key whose partner half was never kept is a lockout.
	var prior *shareBundle
	if tgt.file != nil {
		if old, lerr := tgt.file.load(); lerr == nil {
			prior = old
		}
	}
	shared, code := putShare(tgt, realm, kid, entry)
	if code != 0 {
		return code
	}
	if tgt.stdout {
		fmt.Fprintln(os.Stdout, "::add-mask::"+entry.DPartner)
		writeJSON(map[string]any{"kid": kid, "share": shared.bundle})
	}
	body := map[string]any{"kid": kid, "n": entry.N, "e": entry.E, "d_ri": bigB64(d.dRI), "lease_id": lease.LeaseID}
	if o := f.vals["overlap"]; o != "" {
		body["overlap_d"] = o
	}
	st, raw, abort = stepUpCall(cfg, http.MethodPost, base, body, f.vals["totp"])
	if abort >= 0 {
		return abort
	}
	if st/100 != 2 && tgt.file != nil { // refused outright: undo the new entry, keep the old store as it was
		if prior != nil {
			_ = tgt.file.store(prior)
		} else {
			_ = os.Remove(tgt.file.path)
		}
	}
	if st/100 == 2 {
		fmt.Fprintf(os.Stderr, "%s complete: new kid %s (fingerprint %s)\n", reason, kid, fingerprint(d.n, d.e))
	}
	return skPrint(st, raw)
}

// ---- downgrade ----

func skDowngrade(cfg *Config, f *verbFlags, realm, base string) int {
	if f.bools["cancel"] {
		return skMutate(cfg, f, http.MethodDelete, base+"/downgrade", nil)
	}
	src := f.vals["cosign-from"]
	if src == "" { // the 24-hour path: nothing to complete a challenge with
		return skMutate(cfg, f, http.MethodPost, base+"/downgrade", map[string]any{})
	}
	if !strings.HasPrefix(src, "file:") {
		return failCode(errors.New("--cosign-from: want file:PATH"), exitUsage)
	}
	kek, err := kekFromEnv()
	if err != nil {
		return failCode(fmt.Errorf("--cosign-from needs the KEK: %w", err), exitSplitSigning)
	}
	sb, err := fileShareStore{path: strings.TrimPrefix(src, "file:"), realm: realm, kek: kek}.load()
	if err != nil {
		return failCode(fmt.Errorf("share unavailable: %w", err), exitSplitSigning)
	}
	kid := f.vals["kid"]
	if kid == "" && len(sb.Shares) == 1 {
		for k := range sb.Shares {
			kid = k
		}
	}
	if kid == "" { // several shares: the realm's current kid decides
		var s struct {
			KID string `json:"kid"`
		}
		st, raw, cerr := bffCall(cfg, http.MethodGet, base, nil)
		if cerr != nil {
			return callExit(cerr)
		}
		if st/100 != 2 {
			return skPrint(st, raw)
		}
		decodeBody(raw, &s)
		kid = s.KID
	}
	ent, ok := sb.Shares[kid]
	if !ok {
		return failCode(fmt.Errorf("share unavailable: the share store has no entry for kid %q", kid), exitSplitSigning)
	}
	st, raw, abort := stepUpCall(cfg, http.MethodPost, base+"/downgrade/challenge", map[string]any{}, f.vals["totp"])
	if abort >= 0 {
		return abort
	}
	if st/100 != 2 {
		return skPrint(st, raw)
	}
	var ch struct {
		Nonce     string `json:"nonce"`
		PartialRI string `json:"partial_ri"`
	}
	decodeBody(raw, &ch)
	pri, derr := rawB64.DecodeString(ch.PartialRI)
	if ch.Nonce == "" || derr != nil {
		return failCode(errors.New("the downgrade challenge is malformed"), exitSplitSigning)
	}
	digest := sha256.Sum256([]byte("realmid-split-downgrade/v1|" + realm + "|" + kid + "|" + ch.Nonce))
	cosig, cerr := completeDigest(ent, digest[:], pri)
	if cerr != nil {
		return failCode(fmt.Errorf("completing the downgrade challenge: %w", cerr), exitSplitSigning)
	}
	return skMutate(cfg, f, http.MethodPost, base+"/downgrade", map[string]any{"cosig": rawB64.EncodeToString(cosig)})
}
