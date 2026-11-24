package main

// Ruling 64 (plan 2026-11-25 §2, §8.4): federation-binding create and revoke are person-only,
// and the ISSUER decides whether a code is needed. The CLI no longer mirrors a BFF step-up row for
// them (the BFF drops S4/S5), so the two routes leave the BFF-parity list but stay refused locally
// for a key or workload session (exit 8, nothing sent), and a person's 412 still goes through
// --totp or a prompt and exactly one retry.

import (
	"net/http"
	"strings"
	"testing"
)

type fedBindVerb struct {
	name, method, path string
	args               []string
}

// fedBindVerbs are the two operations as generated verbs. The args after the verb are the
// path-param flags the generated command declares, filled from the command's own Params.
func fedBindVerbs(t *testing.T) []fedBindVerb {
	t.Helper()
	cmds, _, err := buildCommands()
	if err != nil {
		t.Fatal(err)
	}
	var out []fedBindVerb
	for _, cmd := range cmds {
		isCreate := cmd.Method == "POST" && cmd.Path == "/platforms/{id}/federation-bindings"
		isRevoke := cmd.Method == "DELETE" && cmd.Path == "/platforms/{id}/federation-bindings/{bid}"
		if !isCreate && !isRevoke {
			continue
		}
		args := append([]string{}, cmd.Group...)
		args = append(args, cmd.Verb)
		for _, p := range cmd.Params {
			args = append(args, "--"+p.Name, "x")
		}
		if isCreate {
			args = append(args, "--json", `{"issuer":"https://i.example","match_claims":{"sub":"s"},"mapped_role":"member"}`)
		}
		out = append(out, fedBindVerb{name: strings.Join(cmd.Group, " ") + " " + cmd.Verb, method: cmd.Method, path: cmd.Path, args: args})
	}
	if len(out) != 2 {
		t.Fatalf("found %d federation-binding create/revoke verbs, want 2", len(out))
	}
	return out
}

// The two routes are off the BFF-parity list: the BFF has no row for them any more, so the copy
// must not either (RED today: the copy still lists them; TestStepUpRoutesMatchTheBFFList moves
// with the api change).
func TestFedBindRoutesLeaveTheBFFParityList(t *testing.T) {
	for _, r := range stepUpRoutes {
		if strings.HasPrefix(r.Path, "/platforms/{id}/federation-bindings") {
			t.Errorf("stepUpRoutes still lists %s %s; it belongs on the local person-only list, not the BFF-parity list", r.Method, r.Path)
		}
	}
}

// A key session and a workload session (any actor role) exit 8 needs_human on both operations,
// through the generated verb and through `api`, with nothing sent. (Passes today: the routes are
// on the step-up list. It pins that moving them off that list does not open them.)
func TestPersonOnlyRoutes_KeySessionExits8(t *testing.T) {
	f := newBFF(t, nil)
	verbs := fedBindVerbs(t)

	t.Setenv("REALM_ID_API_KEY", "uk_live_abc")
	for _, v := range verbs {
		if _, stderr, code := runCLI(t, v.args...); code != exitNeedsHuman || !strings.Contains(stderr, "needs_human") {
			t.Errorf("key session %v: exit %d, want %d needs_human\n%s", v.args, code, exitNeedsHuman, stderr)
		}
	}
	if _, stderr, code := runCLI(t, "api", "POST", "/api/platforms/p1/federation-bindings", `{}`); code != exitNeedsHuman {
		t.Errorf("key session api POST: exit %d, want %d\n%s", code, exitNeedsHuman, stderr)
	}
	if _, stderr, code := runCLI(t, "api", "DELETE", "/api/platforms/p1/federation-bindings/b1"); code != exitNeedsHuman {
		t.Errorf("key session api DELETE: exit %d, want %d\n%s", code, exitNeedsHuman, stderr)
	}
	t.Setenv("REALM_ID_API_KEY", "")

	for _, role := range []string{"platform_mgmt_api", "member", "platform_api"} {
		sessionConfig(t, f.srv.URL, "workload", role)
		for _, v := range verbs {
			if _, stderr, code := runCLI(t, v.args...); code != exitNeedsHuman {
				t.Errorf("workload(%s) %v: exit %d, want %d (the platform_mgmt_api exception covers platform keys only)\n%s", role, v.args, code, exitNeedsHuman, stderr)
			}
		}
	}
	if n := len(f.all()); n != 0 {
		t.Errorf("%d requests sent by refused calls, want none: %v", n, f.paths())
	}
}

// A person's session: the issuer's op-bound 412 (relayed by the BFF) takes the code from --totp,
// verifies once, adopts the new session token and retries the SAME request once with it. This is
// the red half: with the routes off the step-up list a plain pass-through would print the 412 and
// never verify (RED if W3 forgets to keep these two routes on the person-only list).
func TestStepUpCall_IssuerChallengeRetried(t *testing.T) {
	for _, v := range fedBindVerbs(t) {
		t.Run(v.name+" "+v.method, func(t *testing.T) {
			var gatedBearers []string
			verifyHits := 0
			f := newBFF(t, func(w http.ResponseWriter, r recReq) {
				switch {
				case r.Path == "/auth/mfa/verify":
					verifyHits++
					reply(w, 201, `{"status":"ok","session_token":"rsid_new"}`)
				case strings.Contains(r.Path, "federation-bindings"):
					b := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
					gatedBearers = append(gatedBearers, b)
					if b == "rsid_human" {
						reply(w, 412, `{"error":{"code":"mfa_required","message":"fresh TOTP for this operation"},"mfa_challenge_token":"op-chal","methods":["totp"],"reason":"fresh_required"}`)
						return
					}
					reply(w, 200, `{"ok":true}`)
				default:
					reply(w, 200, `{}`)
				}
			})
			args := append(append([]string{}, v.args...), "--totp", "123456")
			_, stderr, code := runCLI(t, args...)
			if code != exitOK {
				t.Fatalf("exit = %d, want 0\n%s", code, stderr)
			}
			if verifyHits != 1 {
				t.Errorf("verify hits = %d, want exactly 1 (%v)", verifyHits, f.paths())
			}
			if len(gatedBearers) != 2 || gatedBearers[0] != "rsid_human" || gatedBearers[1] != "rsid_new" {
				t.Errorf("binding route bearers = %v, want [rsid_human rsid_new]: one send, one retry with the verified session", gatedBearers)
			}
		})
	}
}

// A person with no --totp and no terminal gets exit 3 (incomplete) and verify is never called;
// the issuer's 412 is not swallowed as a success.
func TestStepUpCall_IssuerChallengeWithoutCodeIsIncomplete(t *testing.T) {
	for _, v := range fedBindVerbs(t) {
		t.Run(v.name+" "+v.method, func(t *testing.T) {
			f := newBFF(t, func(w http.ResponseWriter, r recReq) {
				reply(w, 412, `{"error":{"code":"mfa_required"},"mfa_challenge_token":"op-chal","methods":["totp"]}`)
			})
			_, _, code := runCLI(t, v.args...)
			if code != exitIncomplete {
				t.Errorf("exit = %d, want %d", code, exitIncomplete)
			}
			if len(f.find("POST", "/auth/mfa/verify")) != 0 {
				t.Errorf("verify called without a code: %v", f.paths())
			}
		})
	}
}

// `explain needs_human` names both operations, so an agent that exits 8 learns which verbs a
// person must run. (RED today: the text speaks only of platform keys.)
func TestExplain_NeedsHumanNamesBothFederationBindingOperations(t *testing.T) {
	o, code := runExplain(t, "--output", "json", "needs_human")
	if code != exitOK || len(o.Matches) == 0 {
		t.Fatalf("code=%d matches=%d", code, len(o.Matches))
	}
	var text strings.Builder
	for _, m := range o.Matches {
		text.WriteString(m.Meaning + " " + m.WhatToDo + " ")
	}
	got := text.String()
	for _, want := range []string{"federation-bindings create", "federation-bindings revoke"} {
		if !strings.Contains(got, want) {
			t.Errorf("explain needs_human does not name %q: %s", want, got)
		}
	}
}
