package main

// CLI credentials, session classes, step-up and exit codes for split signing v3:
// plan v3 §3.9 (BFF-only R6/R63; rk_live_ refused locally; needs_human exit 8 except R72's mgmt
// rows; G19 mapping), §7.2 (exit 6 = platform_bearer_required/401, exit 7 = actor_required/403),
// v2 §5.1 + W1.10 (no service mode, key-session, TOTP retry on 412, exit 8/9), R72, R73, R40/R41.
//
// NOTE for the builder: main_test.go TestExitForStatus (401 -> exitForbidden) and
// commands_test.go TestResolveCredential_ServiceMode* / doctor service-mode cases encode the
// behaviour this release removes; they must be changed with the code (R57 keeps `doctor`'s read).

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestExit_G19Mapping(t *testing.T) { // plan v3 §3.9 / §7.2
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"401 platform_bearer_required -> 6", 401, `{"error":{"code":"platform_bearer_required","message":"x"}}`, 6},
		{"any other 401 is 6 too (unauthenticated, split from 7)", 401, `{"error":{"code":"session_expired","message":"x"}}`, 6},
		{"403 actor_required -> 7", 403, `{"error":{"code":"actor_required","message":"x"}}`, 7},
		{"plain 403 stays 7", 403, `{"error":{"code":"forbidden","message":"x"}}`, 7},
		{"409 -> 4", 409, `{"error":{"code":"rotation_lease_held","message":"x"}}`, 4},
		{"404 -> 5", 404, `{"error":{"code":"platform_not_found","message":"x"}}`, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newBFF(t, func(w http.ResponseWriter, r recReq) { reply(w, tc.status, tc.body) })
			_, _, code := runCLI(t, "api", "GET", "/me")
			if code != tc.want {
				t.Fatalf("exit = %d, want %d", code, tc.want)
			}
		})
	}
}

func TestExit_UnauthenticatedIsSixOnTheGenericApiVerbToo(t *testing.T) {
	newBFF(t, func(w http.ResponseWriter, r recReq) {
		reply(w, 401, `{"error":{"code":"platform_bearer_required","message":"x"}}`)
	})
	if _, _, code := runCLI(t, "api", "GET", "/me"); code != 6 {
		t.Fatalf("`api GET` on 401 exit = %d, want 6", code)
	}
}

func TestExplain_KnowsTheNewV3Codes(t *testing.T) { // v2 §5.3 + G19 rows, offline
	for _, code := range []string{"actor_required", "platform_bearer_required", "needs_human",
		"rotation_lease_held", "handshake_failed", "split_upload_not_allowed"} {
		out, c := runExplain(t, code)
		if c != exitOK || len(out.Matches) == 0 {
			t.Errorf("explain %s: exit %d, %d matches; want a row for every v3 code", code, c, len(out.Matches))
		}
	}
}

// ---- credentials: rk_live_ refused, no service mode ----

func TestRKLiveInAPIKeyIsRefusedOfflineWithTheMigrationMessage(t *testing.T) { // v2 §5.1 / R24
	f := newBFF(t, nil)
	t.Setenv("REALM_ID_API_KEY", "rk_live_abc")
	_, stderr, code := runCLI(t, "platforms", "list-mine")
	if code != exitUsage {
		t.Fatalf("exit = %d, want 2", code)
	}
	for _, want := range []string{"REALM_ID_API_KEY holds a platform key (rk_live_)", "user-api-keys create"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
	}
	if n := len(f.all()); n != 0 {
		t.Errorf("rk_live_ caused %d network calls, want 0 (refused offline)", n)
	}
}

func TestUserKeyBecomesAKeySession_NoServiceModeNoIssuerCalls(t *testing.T) { // R6/R63, R57
	f := newBFF(t, func(w http.ResponseWriter, r recReq) {
		if r.Path == "/auth/key-session" {
			reply(w, 200, `{"session_token":"rsid_key","session_class":"key"}`)
			return
		}
		reply(w, 200, `[]`)
	})
	t.Setenv("REALM_ID_API_KEY", "uk_live_abc")
	if _, stderr, code := runCLI(t, "platforms", "list-mine"); code != exitOK {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	ks := f.find("POST", "/auth/key-session")
	if len(ks) != 1 || !strings.Contains(string(ks[0].Body), "uk_live_abc") {
		t.Fatalf("requests = %v, want one POST /auth/key-session carrying the key in its body", f.paths())
	}
	if ks[0].Header.Get("Authorization") != "" {
		t.Error("the key travelled as a bearer; it belongs in the body")
	}
	got := f.find("GET", "/platforms/mine")
	if len(got) != 1 || got[0].Header.Get("Authorization") != "Bearer rsid_key" {
		t.Errorf("typed call = %v, want GET …/platforms/mine with the key session bearer", f.paths())
	}
	for _, p := range f.paths() {
		if strings.Contains(p, "/auth/login") {
			t.Errorf("the CLI still signs in at the issuer: %s", p)
		}
	}
}

// ---- needs_human (exit 8): R40, R73 ----

func TestKeySession_HumanOnlyVerbsExitEightAndSendNothing(t *testing.T) {
	humanOnly := [][]string{
		{"user-api-keys", "create", "--tenant", "t1", "--label", "ci", "--permissions", "a:b"},
		{"signing-keys", "upload", "--realm", v3Realm, "--bundle", "/nonexistent"},
		{"signing-keys", "extend", "--realm", v3Realm, "--by", "24h"},
		{"signing-keys", "cancel", "--realm", v3Realm},
		{"signing-keys", "downgrade", "--realm", v3Realm},
		{"signing-keys", "rotate", "--realm", v3Realm},
		{"signing-keys", "recover", "--realm", v3Realm},
		{"platform-keys", "create", "--realm", v3Realm, "--label", "x"},
	}
	for _, args := range humanOnly {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			f := newBFF(t, nil)
			t.Setenv("REALM_ID_API_KEY", "uk_live_abc")
			stdout, stderr, code := runCLI(t, args...)
			if code != 8 {
				t.Fatalf("exit = %d, want 8 (needs_human)\nstderr: %s", code, stderr)
			}
			if n := len(f.all()); n != 0 {
				t.Errorf("%d requests sent (%v); needs_human sends nothing, not even the key exchange", n, f.paths())
			}
			if stdout != "" {
				t.Errorf("stdout must stay empty, got %q", stdout)
			}
			var env struct {
				Error struct{ Code, Message, Hint string } `json:"error"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(stderr)), &env); err != nil {
				t.Fatalf("stderr is not the JSON error envelope: %v\n%s", err, stderr)
			}
			if env.Error.Code != "needs_human" || !strings.Contains(env.Error.Message, "needs a person") || env.Error.Hint == "" {
				t.Errorf("envelope = %+v, want code needs_human, message 'needs a person…', a hint", env.Error)
			}
		})
	}
}

func TestKeySession_ReadVerbsStillWork(t *testing.T) { // R40 lists mutations only; status is a read
	newBFF(t, func(w http.ResponseWriter, r recReq) {
		if r.Path == "/auth/key-session" {
			reply(w, 200, `{"session_token":"rsid_key"}`)
			return
		}
		reply(w, 200, `{"state":"split"}`)
	})
	t.Setenv("REALM_ID_API_KEY", "uk_live_abc")
	if _, _, code := runCLI(t, "signing-keys", "status", "--realm", v3Realm); code != exitOK {
		t.Errorf("signing-keys status in a key session exit = %d, want 0", code)
	}
}

// Workload (WIF) sessions: R72 lets a platform_mgmt_api actor mint/revoke platform keys; every
// other workload actor is needs_human. [Wire inferred: GitHub's own OIDC env vars; the BFF body
// carries the OIDC token; the response names the session class and, for R72, the actor role.]
func workloadLogin(t *testing.T, role string) *v3BFF {
	f := newBFF(t, nil)
	f.handle = func(w http.ResponseWriter, r recReq) {
		switch {
		case r.Path == "/oidc":
			reply(w, 200, `{"value":"gh.oidc.jwt"}`)
		case r.Path == "/auth/workload-session":
			reply(w, 200, `{"session_token":"rsid_wl","session_class":"workload","role":"`+role+`"}`)
		default:
			reply(w, 200, `{"id":"k"}`)
		}
	}
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", f.srv.URL+"/oidc?x=1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "gh-req-token")
	if _, stderr, code := runCLI(t, "auth", "workload", "--audience", "https://api.realmid.dev"); code != exitOK {
		t.Fatalf("auth workload exit = %d\n%s", code, stderr)
	}
	ws := f.find("POST", "/auth/workload-session")
	if len(ws) != 1 || !strings.Contains(string(ws[0].Body), "gh.oidc.jwt") {
		t.Fatalf("requests = %v, want one POST /auth/workload-session carrying the OIDC token", f.paths())
	}
	return f
}

func TestWorkloadSession_PlatformKeysAreAllowedOnlyForTheMgmtActor(t *testing.T) { // R72
	f := workloadLogin(t, "platform_mgmt_api")
	before := len(f.all())
	if _, stderr, code := runCLI(t, "platform-keys", "create", "--realm", v3Realm, "--label", "rotated"); code != exitOK {
		t.Fatalf("platform-keys create as platform_mgmt_api exit = %d\n%s", code, stderr)
	}
	post := f.all()[before:]
	if len(post) == 0 || post[len(post)-1].Method != "POST" || !strings.HasSuffix(post[len(post)-1].Path, "/api-keys") ||
		post[len(post)-1].Header.Get("Authorization") != "Bearer rsid_wl" {
		t.Errorf("requests after login = %v, want a POST …/api-keys with the workload bearer", f.paths()[before:])
	}
}

func TestWorkloadSession_AnyOtherActorOrVerbIsNeedsHuman(t *testing.T) {
	// A plain service-account workload session may not mint platform keys (R72: that actor only).
	f := workloadLogin(t, "member")
	before := len(f.all())
	if _, _, code := runCLI(t, "platform-keys", "create", "--realm", v3Realm, "--label", "x"); code != 8 {
		t.Errorf("platform-keys create as a plain SA exit = %d, want 8", code)
	}
	// Even the mgmt actor gets needs_human on every OTHER R40 verb.
	f2 := workloadLogin(t, "platform_mgmt_api")
	before2 := len(f2.all())
	if _, _, code := runCLI(t, "user-api-keys", "create", "--tenant", "t1", "--label", "ci", "--permissions", "a:b"); code != 8 {
		t.Errorf("user-api-keys create in a workload session exit = %d, want 8", code)
	}
	if _, _, code := runCLI(t, "signing-keys", "rotate", "--realm", v3Realm); code != 8 {
		t.Errorf("signing-keys rotate in a workload session exit = %d, want 8 (CI rotation path dropped, R73)", code)
	}
	if n := len(f.all()) - before; n != 0 {
		t.Errorf("%d requests sent after a refused verb: %v", n, f.paths()[before:])
	}
	if n := len(f2.all()) - before2; n != 0 {
		t.Errorf("%d requests sent after a refused verb: %v", n, f2.paths()[before2:])
	}
}

// ---- step-up: 412 -> TOTP -> retry (R40/R41) ----

func stepUpBFF(t *testing.T, verifyStatus int) *v3BFF {
	creates := 0
	return newBFF(t, func(w http.ResponseWriter, r recReq) {
		switch {
		case strings.HasSuffix(r.Path, "/user-api-keys") && r.Method == "POST":
			creates++
			if creates == 1 {
				reply(w, 412, `{"error":{"code":"step_up_required","message":"x","mfa_challenge_token":"mc_1"}}`)
				return
			}
			reply(w, 201, `{"id":"uk_1","label":"ci"}`)
		case r.Path == "/auth/mfa/verify":
			reply(w, verifyStatus, `{}`)
		default:
			reply(w, 200, `{}`)
		}
	})
}

func TestStepUp_412WithATotpCodeVerifiesThenRetriesTheSameRequest(t *testing.T) {
	f := stepUpBFF(t, 200)
	_, stderr, code := runCLI(t, "user-api-keys", "create", "--tenant", "t1", "--label", "ci",
		"--permissions", "a:b", "--totp", "123456")
	if code != exitOK {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	creates := f.find("POST", "/user-api-keys")
	vi := f.idx("POST", "/auth/mfa/verify")
	if len(creates) != 2 || vi < 0 {
		t.Fatalf("requests = %v, want create, mfa/verify, create", f.paths())
	}
	v := string(f.find("POST", "/auth/mfa/verify")[0].Body)
	if !strings.Contains(v, "mc_1") || !strings.Contains(v, "123456") {
		t.Errorf("mfa/verify body = %s, want the challenge token and the code", v)
	}
	if string(creates[0].Body) != string(creates[1].Body) {
		t.Errorf("the retry changed the request body:\n%s\n%s", creates[0].Body, creates[1].Body)
	}
}

func TestStepUp_412WithNoCodeOffTTYIsIncompleteExitThree(t *testing.T) {
	f := stepUpBFF(t, 200)
	_, stderr, code := runCLI(t, "user-api-keys", "create", "--tenant", "t1", "--label", "ci", "--permissions", "a:b")
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (incomplete)\n%s", code, stderr)
	}
	if len(f.find("POST", "/auth/mfa/verify")) != 0 || len(f.find("POST", "/user-api-keys")) != 1 {
		t.Errorf("requests = %v, want one create and no verify", f.paths())
	}
}
