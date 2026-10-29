package main

// `realm-id signing-keys …` (G14; plan v3 §3.9/§7.2; v2 §5.1 + W1.10; R9, R36, R38, R39, R73;
// owner rulings 2026-10-29 C5#1 lease ticket, C5#4 downgrade challenge). All verbs go through the
// BFF `/api/*` passthrough with the human session bearer; none touches the issuer directly.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"os"
	"strings"
	"testing"
)

func lifecycleBFF(t *testing.T, extra func(w http.ResponseWriter, r recReq) bool) *v3BFF {
	return newBFF(t, func(w http.ResponseWriter, r recReq) {
		if extra != nil && extra(w, r) {
			return
		}
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.Path, "/rotation-lease"):
			reply(w, 200, `{"lease_id":"ls_1","expires_at":"2026-10-29T12:05:00Z"}`)
		default:
			reply(w, 200, `{"ok":true}`)
		}
	})
}

// W1.10 / R9: the share must go somewhere named; stdout only when asked for; offline keygen.
func TestSigningKeysInit_RefusesAnUnnamedOrUnprotectedShareTarget(t *testing.T) {
	f := newBFF(t, nil)
	// No --share-to at all: there is no default, and no stdout fallback (R9).
	_, _, code := runCLI(t, "signing-keys", "init", "--realm", v3Realm, "--kid", "k1", "--out", t.TempDir())
	if code != exitUsage {
		t.Fatalf("init without --share-to exit = %d, want 2 (usage)", code)
	}
	// file: with no KEK outside local dev (SPEC §14.5) and redis: with no KEK (G13) are refused.
	for _, target := range []string{"file:" + t.TempDir() + "/s.json", "redis:redis://127.0.0.1:1/0"} {
		_, stderr, code := runCLI(t, "signing-keys", "init", "--realm", v3Realm, "--kid", "k1",
			"--share-to", target, "--out", t.TempDir())
		if code != exitUsage {
			t.Errorf("init --share-to %s without REALM_ID_SHARE_KEK exit = %d, want 2", target, code)
		}
		if !strings.Contains(stderr, "REALM_ID_SHARE_KEK") {
			t.Errorf("stderr does not name REALM_ID_SHARE_KEK: %q", stderr)
		}
	}
	if n := len(f.all()); n != 0 {
		t.Errorf("init made %d network calls, want 0 (offline keygen, R5)", n)
	}
}

func TestSigningKeysInit_FileShareIsASoundSplitAndTheOutputsAreRight(t *testing.T) {
	f := newBFF(t, nil)
	m := runInit(t, "k-2026-10")
	if n := len(f.all()); n != 0 {
		t.Fatalf("init made %d network calls, want 0", n)
	}
	// d_ri and d_partner together must sign what the public key verifies (R5 dealer split).
	digest := sha256.Sum256([]byte("any message"))
	partRI := b64i(m.partialRI(digest[:]))
	n := b64i(m.N)
	k := (n.BitLen() + 7) / 8
	sP := new(big.Int).Exp(emsa(digest[:], k), b64i(m.DPartner), n)
	sig := new(big.Int).Mul(partRI, sP)
	sig.Mod(sig, n)
	if err := verifyDigestSig(m, digest[:], base64.RawURLEncoding.EncodeToString(sig.FillBytes(make([]byte, k)))); err != nil {
		t.Fatalf("partial_ri * partner partial does not verify under the public key: %v", err)
	}
	// The share file is mode 0600 (SPEC §14.5).
	if st, err := os.Stat(m.ShareFile); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("share file mode = %v (err %v), want 0600", st.Mode().Perm(), err)
	}
	// RealmID's bundle carries no partner half; the public-only list carries no private field (R39).
	braw, _ := os.ReadFile(m.BundlePath)
	if strings.Contains(string(braw), "d_partner") {
		t.Error("the RealmID-half bundle leaks d_partner")
	}
	pubPath := strings.TrimSuffix(m.BundlePath, "bundle.json") + "public.json"
	praw, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatalf("init wrote no public-only list at %s: %v", pubPath, err)
	}
	var jwks struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(praw, &jwks); err != nil || len(jwks.Keys) != 1 || jwks.Keys[0]["kid"] != "k-2026-10" {
		t.Fatalf("public.json is not a one-key standard JWKS for the new kid: %v\n%s", err, praw)
	}
	for _, priv := range []string{"d", "d_ri", "d_partner", "p", "q"} {
		if _, bad := jwks.Keys[0][priv]; bad {
			t.Errorf("public-only list carries private field %q", priv)
		}
	}
}

func TestSigningKeysInit_PrintsKidFingerprintBitsAndWhereItWrote(t *testing.T) {
	newBFF(t, nil)
	dir := t.TempDir()
	kek := newKEK(t)
	t.Setenv("REALM_ID_SHARE_KEK", kekEnv(kek))
	stdout, _, code := runCLI(t, "signing-keys", "init", "--realm", v3Realm, "--kid", "k9",
		"--share-to", "file:"+dir+"/s.json", "--out", dir)
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if out["kid"] != "k9" || out["n_bits"] != float64(2048) || out["fingerprint"] == "" || out["fingerprint"] == nil || out["written_to"] == nil {
		t.Errorf("init output = %v, want kid, fingerprint, n_bits=2048, written_to", out)
	}
	if strings.Contains(stdout, "d_partner") || strings.Contains(stdout, "d_ri") {
		t.Error("init stdout leaked key material without --share-to stdout")
	}
}

func TestSigningKeysInit_StdoutShareOnlyWhenExplicitAndMasked(t *testing.T) {
	newBFF(t, nil)
	stdout, _, code := runCLI(t, "signing-keys", "init", "--realm", v3Realm, "--kid", "k1",
		"--share-to", "stdout", "--out", t.TempDir())
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	// R9: a CI log mask line precedes the secret so the runner redacts it.
	if !strings.Contains(stdout, "::add-mask::") {
		t.Errorf("explicit stdout share is not preceded by a ::add-mask:: line:\n%s", stdout)
	}
	if !strings.Contains(stdout, "d_partner") {
		t.Errorf("explicit stdout share carries no share:\n%s", stdout)
	}
}

// R73 / v2 §5.1: upload sends n, e, d_ri, kid; the FIRST upload takes no lease; then d_ri is dropped.
func TestSigningKeysUpload_SendsTheRIHalfAndDropsItLocally(t *testing.T) {
	m := runInit(t, "k1")
	f := lifecycleBFF(t, nil)
	_, stderr, code := runCLI(t, "signing-keys", "upload", "--realm", v3Realm, "--bundle", m.BundlePath, "--overlap", "24h")
	if code != exitOK {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	ups := f.find("POST", "/signing-keys/split")
	if len(ups) != 1 || ups[0].Path != splitBase() {
		t.Fatalf("requests = %v, want exactly one POST %s", f.paths(), splitBase())
	}
	if got := ups[0].Header.Get("Authorization"); got != "Bearer rsid_human" {
		t.Errorf("upload auth = %q, want the human session bearer (owner, R73)", got)
	}
	body := jsonBody(t, ups[0])
	if body["kid"] != "k1" || body["n"] != m.N || body["e"] != m.E || body["d_ri"] != m.DRI || body["overlap_d"] != "24h" {
		t.Errorf("upload body = %v, want kid,n,e,d_ri,overlap_d=24h", body)
	}
	if _, has := body["lease_id"]; has {
		t.Error("the FIRST upload must not carry lease_id (C5#1: exempt)")
	}
	if len(f.find("POST", "/rotation-lease")) != 0 {
		t.Error("the FIRST upload took a lease; it is exempt")
	}
	// Dropped locally: the RealmID half is gone from the bundle file after a successful upload.
	braw, _ := os.ReadFile(m.BundlePath)
	if strings.Contains(string(braw), m.DRI) || strings.Contains(string(braw), `"d_ri"`) {
		t.Error("d_ri still on disk after a successful upload")
	}
}

func TestSigningKeysUpload_FailureKeepsTheHalfAndMapsTheExit(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   int
	}{
		{409, `{"error":{"code":"split_kid_in_use","message":"x"}}`, exitConflict},
		{409, `{"error":{"code":"split_upload_not_allowed","message":"x"}}`, exitConflict}, // C5#5
		{403, `{"error":{"code":"actor_required","message":"x"}}`, 7},                      // bare platform token (R73/G19)
		{401, `{"error":{"code":"platform_bearer_required","message":"x"}}`, 6},
	} {
		m := runInit(t, "k1")
		lifecycleBFF(t, func(w http.ResponseWriter, r recReq) bool {
			reply(w, tc.status, tc.body)
			return true
		})
		_, _, code := runCLI(t, "signing-keys", "upload", "--realm", v3Realm, "--bundle", m.BundlePath)
		if code != tc.want {
			t.Errorf("upload answered %d %s: exit = %d, want %d", tc.status, tc.body, code, tc.want)
		}
		if braw, _ := os.ReadFile(m.BundlePath); !strings.Contains(string(braw), m.DRI) {
			t.Errorf("upload failed (%d) but d_ri was dropped from the bundle: it is the only copy", tc.status)
		}
	}
}

func TestSigningKeysStatus_ReadsTheSplitRouteAndPrintsJSON(t *testing.T) {
	f := newBFF(t, func(w http.ResponseWriter, r recReq) {
		reply(w, 200, `{"state":"overlap","kid":"k1","overlap_until":"2026-10-30T00:00:00Z"}`)
	})
	stdout, _, code := runCLI(t, "signing-keys", "status", "--realm", v3Realm)
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if got := f.paths(); len(got) != 1 || got[0] != "GET "+splitBase() {
		t.Fatalf("requests = %v, want GET %s", got, splitBase())
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil || out["state"] != "overlap" {
		t.Errorf("status stdout = %q (err %v), want JSON carrying state=overlap", stdout, err)
	}
}

func TestSigningKeysPin_PrintsAPublicOnlyStandardJWKS(t *testing.T) { // R23 + C5#6
	m := runInit(t, "k1")
	newBFF(t, func(w http.ResponseWriter, r recReq) {
		reply(w, 200, `{"state":"split","kid":"k1","public_key":{"n":"`+m.N+`","e":"`+m.E+`"}}`)
	})
	stdout, _, code := runCLI(t, "signing-keys", "pin", "--realm", v3Realm)
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	var jwks struct {
		Keys []map[string]any `json:"keys"`
	}
	// C5#6: the base-trust secret is a standard JWKS object; a bare array is refused downstream.
	if err := json.Unmarshal([]byte(stdout), &jwks); err != nil || len(jwks.Keys) != 1 {
		t.Fatalf("pin output is not a {keys:[…]} JWKS: %v\n%s", err, stdout)
	}
	if jwks.Keys[0]["kid"] != "k1" || jwks.Keys[0]["n"] != m.N || jwks.Keys[0]["kty"] != "RSA" {
		t.Errorf("pinned key = %v", jwks.Keys[0])
	}
	if _, bad := jwks.Keys[0]["d"]; bad {
		t.Error("pin output carries a private field")
	}
}

func TestSigningKeysExtendAndCancel(t *testing.T) { // v2 §5.1, R36 (cancel also in overlap_pending, C5#2)
	f := lifecycleBFF(t, nil)
	if _, _, code := runCLI(t, "signing-keys", "extend", "--realm", v3Realm, "--by", "24h"); code != exitOK {
		t.Fatalf("extend exit = %d", code)
	}
	if _, _, code := runCLI(t, "signing-keys", "cancel", "--realm", v3Realm); code != exitOK {
		t.Fatalf("cancel exit = %d", code)
	}
	ext := f.find("POST", "/split/overlap/extend")
	if len(ext) != 1 || jsonBody(t, ext[0])["by"] != "24h" {
		t.Errorf("extend requests = %v", f.paths())
	}
	if len(f.find("POST", "/split/overlap/cancel")) != 1 {
		t.Errorf("cancel requests = %v", f.paths())
	}
	lifecycleBFF(t, func(w http.ResponseWriter, r recReq) bool {
		reply(w, 409, `{"error":{"code":"not_in_overlap","message":"x"}}`)
		return true
	})
	if _, _, code := runCLI(t, "signing-keys", "cancel", "--realm", v3Realm); code != exitConflict {
		t.Errorf("cancel in a wrong state exit = %d, want 4", code)
	}
}

// Owner ruling C5#1: rotate takes the lease ITSELF, then uploads with its lease_id.
func TestSigningKeysRotate_TakesTheLeaseThenUploadsWithItsLeaseID(t *testing.T) {
	m := runInit(t, "k1")
	f := lifecycleBFF(t, nil)
	t.Setenv("REALM_ID_SHARE_KEK", kekEnv(m.KEK)) // newBFF cleared it
	_, stderr, code := runCLI(t, "signing-keys", "rotate", "--realm", v3Realm, "--kid", "k2",
		"--share-to", "file:"+m.ShareFile)
	if code != exitOK {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	li, ui := f.idx("POST", "/rotation-lease"), f.idx("POST", "/signing-keys/split")
	if li < 0 || ui < 0 || li > ui {
		t.Fatalf("request order = %v, want rotation-lease BEFORE the upload", f.paths())
	}
	if got := jsonBody(t, f.find("POST", "/rotation-lease")[0]); got["reason"] != "rotation" { // R92
		t.Errorf("lease body = %v, want reason=rotation", got)
	}
	up := jsonBody(t, f.find("POST", "/signing-keys/split")[0])
	if up["lease_id"] != "ls_1" || up["kid"] != "k2" || up["d_ri"] == nil || up["d_ri"] == "" {
		t.Errorf("upload body = %v, want kid=k2, d_ri and lease_id=ls_1", up)
	}
	// The new share is written to the SAME store the old one lives in.
	raw, _ := os.ReadFile(m.ShareFile)
	if _, ok := openEnvelope(t, raw, m.KEK, v3Realm).Shares["k2"]; !ok {
		t.Error("the share store has no entry for the new kid k2 after rotate")
	}
}

func TestSigningKeysRotate_LeaseHeldIsExitFourAndNothingIsUploaded(t *testing.T) {
	m := runInit(t, "k1")
	f := lifecycleBFF(t, func(w http.ResponseWriter, r recReq) bool {
		if strings.HasSuffix(r.Path, "/rotation-lease") {
			reply(w, 409, `{"error":{"code":"rotation_lease_held","message":"another rotation is under way"}}`)
			return true
		}
		return false
	})
	t.Setenv("REALM_ID_SHARE_KEK", kekEnv(m.KEK)) // newBFF cleared it
	_, _, code := runCLI(t, "signing-keys", "rotate", "--realm", v3Realm, "--share-to", "file:"+m.ShareFile)
	if code != exitConflict {
		t.Fatalf("exit = %d, want 4 (conflict)", code)
	}
	if len(f.find("POST", "/signing-keys/split")) != 0 {
		t.Error("rotate uploaded after the lease was refused")
	}
}

func TestSigningKeysRotate_UploadRefusedForLeaseHeldIsExitFour(t *testing.T) {
	m := runInit(t, "k1")
	lifecycleBFF(t, func(w http.ResponseWriter, r recReq) bool {
		if r.Path == splitBase() {
			reply(w, 409, `{"error":{"code":"rotation_lease_held","message":"x"}}`)
			return true
		}
		return false
	})
	t.Setenv("REALM_ID_SHARE_KEK", kekEnv(m.KEK)) // newBFF cleared it
	_, _, code := runCLI(t, "signing-keys", "rotate", "--realm", v3Realm, "--share-to", "file:"+m.ShareFile)
	if code != exitConflict {
		t.Fatalf("exit = %d, want 4", code)
	}
}

// R39 + R92: recover is a recovery-reason lease, then an upload with lease_id; prints the new
// value (env source) masked.
func TestSigningKeysRecover_LeaseReasonRecoveryThenUploadWithLeaseID(t *testing.T) {
	newBFF(t, nil)
	t.Setenv("REALM_ID_SHARE_KEK", kekEnv(newKEK(t)))
	f := lifecycleBFF(t, nil)
	stdout, stderr, code := runCLI(t, "signing-keys", "recover", "--realm", v3Realm, "--kid", "k3")
	if code != exitOK {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	li, ui := f.idx("POST", "/rotation-lease"), f.idx("POST", "/signing-keys/split")
	if li < 0 || ui < 0 || li > ui {
		t.Fatalf("request order = %v, want lease before upload", f.paths())
	}
	if got := jsonBody(t, f.find("POST", "/rotation-lease")[0]); got["reason"] != "recovery" {
		t.Errorf("lease body = %v, want reason=recovery", got)
	}
	if up := jsonBody(t, f.find("POST", "/signing-keys/split")[0]); up["lease_id"] != "ls_1" || up["kid"] != "k3" {
		t.Errorf("upload body = %v, want lease_id=ls_1 kid=k3", up)
	}
	if !strings.Contains(stdout, "::add-mask::") || !strings.Contains(stdout, "k3") {
		t.Errorf("recover must print the new value masked, naming the kid:\n%s", stdout)
	}
}

func TestSigningKeysRecover_EmergencyCapStopsBeforeAnyUpload(t *testing.T) {
	f := lifecycleBFF(t, func(w http.ResponseWriter, r recReq) bool {
		if strings.HasSuffix(r.Path, "/rotation-lease") {
			reply(w, 429, `{"error":{"code":"emergency_rotation_cap_reached","message":"x"}}`)
			return true
		}
		return false
	})
	t.Setenv("REALM_ID_SHARE_KEK", kekEnv(newKEK(t)))
	_, _, code := runCLI(t, "signing-keys", "recover", "--realm", v3Realm)
	if code != exitErr {
		t.Errorf("exit = %d, want 1", code)
	}
	if len(f.find("POST", "/signing-keys/split")) != 0 {
		t.Error("recover uploaded after the lease was refused")
	}
}

// ---- downgrade (R38; owner ruling C5#4) ----

func downgradeBFF(t *testing.T, m keyMaterial, wantKID string, downgradeStatus int) *v3BFF {
	return newBFF(t, func(w http.ResponseWriter, r recReq) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.Path, "/split/downgrade/challenge"):
			digest := sha256.Sum256([]byte("realmid-split-downgrade/v1|" + v3Realm + "|" + wantKID + "|n-77"))
			reply(w, 200, `{"nonce":"n-77","partial_ri":"`+m.partialRI(digest[:])+`"}`)
		case r.Method == "POST" && strings.HasSuffix(r.Path, "/split/downgrade"):
			var b struct {
				Cosig string `json:"cosig"`
			}
			_ = json.Unmarshal(r.Body, &b)
			digest := sha256.Sum256([]byte("realmid-split-downgrade/v1|" + v3Realm + "|" + wantKID + "|n-77"))
			if b.Cosig != "" && verifyDigestSig(m, digest[:], b.Cosig) != nil {
				reply(w, 401, `{"error":{"code":"handshake_failed","message":"cosig does not verify"}}`)
				return
			}
			reply(w, downgradeStatus, `{"state":"normal"}`)
		default:
			reply(w, 200, `{}`)
		}
	})
}

func TestSigningKeysDowngrade_TakesTheOwnChallengeCompletesItAndSendsCosig(t *testing.T) {
	m := runInit(t, "k1")
	f := downgradeBFF(t, m, "k1", 200)
	t.Setenv("REALM_ID_SHARE_KEK", kekEnv(m.KEK))
	_, stderr, code := runCLI(t, "signing-keys", "downgrade", "--realm", v3Realm, "--cosign-from", "file:"+m.ShareFile)
	if code != exitOK {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	ci, di := f.idx("POST", "/split/downgrade/challenge"), f.idx("POST", "/split/downgrade")
	if ci < 0 || di < 0 || ci > di {
		t.Fatalf("request order = %v, want …/downgrade/challenge BEFORE …/downgrade", f.paths())
	}
	body := jsonBody(t, f.find("POST", "/split/downgrade")[0])
	if len(body) != 1 || body["cosig"] == nil {
		t.Fatalf("downgrade body = %v, want exactly {cosig}", body)
	}
	// The server-side check in the fake already refused a bad cosig with 401; exit 0 proves it
	// verified under realmid-split-downgrade/v1|realm|kid|nonce. Assert the handshake route is unused.
	if len(f.find("POST", "/handshake")) != 0 || len(f.find("POST", "/handshake/complete")) != 0 {
		t.Error("downgrade used the handshake nonce; it has its own challenge (C5#4)")
	}
}

func TestSigningKeysDowngrade_WithoutACosignSourceIsTheTwentyFourHourPathAndTakesNoChallenge(t *testing.T) {
	m := runInit(t, "k1")
	f := downgradeBFF(t, m, "k1", 200)
	_, _, code := runCLI(t, "signing-keys", "downgrade", "--realm", v3Realm)
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if len(f.find("POST", "/downgrade/challenge")) != 0 {
		t.Error("a downgrade with no cosign source took a challenge; there is nothing to complete it with")
	}
	dg := f.find("POST", "/split/downgrade")
	if len(dg) != 1 {
		t.Fatalf("requests = %v", f.paths())
	}
	if _, has := jsonBody(t, dg[0])["cosig"]; has {
		t.Error("cosig sent although no partner half was supplied")
	}
}

func TestSigningKeysDowngrade_CancelIsDeleteOnTheDowngradeRoute(t *testing.T) {
	f := newBFF(t, nil)
	_, _, code := runCLI(t, "signing-keys", "downgrade", "--realm", v3Realm, "--cancel")
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if got := f.paths(); len(got) != 1 || got[0] != "DELETE "+splitBase()+"/downgrade" {
		t.Errorf("requests = %v, want one DELETE …/split/downgrade", got)
	}
}

func TestSigningKeysDowngrade_SplitSigningFailuresAreExitNine(t *testing.T) {
	m := runInit(t, "k1")
	// 401 handshake_failed from the issuer is exit 9, NOT the generic 401 -> 6.
	newBFF(t, func(w http.ResponseWriter, r recReq) {
		if strings.HasSuffix(r.Path, "/downgrade/challenge") {
			reply(w, 200, `{"nonce":"n-1","partial_ri":"`+m.partialRI(make([]byte, 32))+`"}`) // for another digest
			return
		}
		reply(w, 401, `{"error":{"code":"handshake_failed","message":"x"}}`)
	})
	t.Setenv("REALM_ID_SHARE_KEK", kekEnv(m.KEK))
	if _, _, code := runCLI(t, "signing-keys", "downgrade", "--realm", v3Realm, "--cosign-from", "file:"+m.ShareFile); code != 9 {
		t.Errorf("handshake_failed exit = %d, want 9 (split_signing)", code)
	}
	// An unreadable share source is exit 9 too (share unavailable), before anything is sent.
	f := newBFF(t, nil)
	t.Setenv("REALM_ID_SHARE_KEK", kekEnv(m.KEK))
	if _, _, code := runCLI(t, "signing-keys", "downgrade", "--realm", v3Realm, "--cosign-from", "file:/nonexistent/shares.json"); code != 9 {
		t.Errorf("share unavailable exit = %d, want 9", code)
	}
	if len(f.all()) != 0 {
		t.Errorf("requests sent with an unreadable share: %v", f.paths())
	}
}

// ---- removed / non-verbs ----

func TestSigningKeys_RemovedVerbsAreUsageErrors(t *testing.T) { // v2 §5.1 "Removed"
	newBFF(t, nil)
	// Positive control: the family exists, so an unknown-command exit below is about the verb.
	if _, _, code := runCLI(t, "signing-keys", "status", "--realm", v3Realm); code != exitOK {
		t.Fatalf("control: signing-keys status exit = %d, want 0", code)
	}
	for _, verb := range []string{"activate", "repin", "retire", "emergency-retire", "handshake"} {
		if _, _, code := runCLI(t, "signing-keys", verb, "--realm", v3Realm); code != exitUsage {
			t.Errorf("signing-keys %s exit = %d, want 2", verb, code)
		}
	}
	if _, _, code := runCLI(t, "signing", "split", "status"); code != exitUsage {
		t.Errorf("the old `signing split` family answered exit %d, want 2", code)
	}
}
