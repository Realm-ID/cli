package main

// Every generated verb and `realm-id api` call that matches a step-up route applies
// the step-up policy: needs_human (nothing sent) for key and workload sessions, step-up for a person.

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

// sessionConfig rewrites the CLI's config with a saved session of the given class and role.
func sessionConfig(t *testing.T, bff, class, role string) {
	t.Helper()
	body := `{"bff_url":"` + bff + `","session_token":"rsid_s","session_class":"` + class + `","session_role":"` + role + `"}`
	if err := os.WriteFile(os.Getenv("REALM_ID_CONFIG"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAPIVerbIsNeedsHumanInAKeySessionOnAStepUpRoute(t *testing.T) {
	f := newBFF(t, nil)
	t.Setenv("REALM_ID_API_KEY", "uk_live_abc")
	_, stderr, code := runCLI(t, "api", "DELETE", "/api/tenants/t1")
	if code != exitNeedsHuman || !strings.Contains(stderr, "needs_human") {
		t.Fatalf("exit = %d, want %d needs_human\n%s", code, exitNeedsHuman, stderr)
	}
	if n := len(f.all()); n != 0 {
		t.Errorf("%d requests sent, want none: %v", n, f.paths())
	}
}

func TestAPIVerbBodyConditionDecidesTheStepUp(t *testing.T) {
	f := newBFF(t, nil)
	t.Setenv("REALM_ID_API_KEY", "uk_live_abc")
	if _, _, code := runCLI(t, "api", "PATCH", "/api/tenants/t1", `{"Status":"deactivated"}`); code != exitNeedsHuman {
		t.Errorf("deactivating PATCH exit = %d, want %d", code, exitNeedsHuman)
	}
	before := len(f.all())
	runCLI(t, "api", "PATCH", "/api/tenants/t1", `{"name":"renamed"}`)
	if len(f.all()) == before {
		t.Errorf("a rename PATCH was refused locally; it is not a step-up route")
	}
}

func TestAPIVerbOffTheStepUpListIsUnchanged(t *testing.T) {
	f := newBFF(t, nil)
	t.Setenv("REALM_ID_API_KEY", "uk_live_abc")
	runCLI(t, "api", "GET", "/api/tenants/t1")
	if len(f.all()) == 0 {
		t.Error("GET on a route off the list was refused locally")
	}
}

func TestAPIVerbWorkloadSessionAdmitsOnlyTheMgmtActorOnPlatformKeyRoutes(t *testing.T) {
	f := newBFF(t, nil)
	sessionConfig(t, f.srv.URL, "workload", "platform_mgmt_api")
	if _, stderr, code := runCLI(t, "api", "POST", "/api/platforms/p1/api-keys", `{"label":"x"}`); code == exitNeedsHuman {
		t.Errorf("platform_mgmt_api refused on platform-key mint\n%s", stderr)
	}
	if _, _, code := runCLI(t, "api", "DELETE", "/api/tenants/t1"); code != exitNeedsHuman {
		t.Errorf("platform_mgmt_api on tenant delete exit = %d, want %d", code, exitNeedsHuman)
	}
	sessionConfig(t, f.srv.URL, "workload", "member")
	if _, _, code := runCLI(t, "api", "POST", "/api/platforms/p1/api-keys", `{"label":"x"}`); code != exitNeedsHuman {
		t.Errorf("a service-account workload on platform-key mint exit = %d, want %d", code, exitNeedsHuman)
	}
}

func TestAPIVerbPersonOnAStepUpRouteIsChallengedNotPassedThrough(t *testing.T) {
	f := newBFF(t, func(w http.ResponseWriter, r recReq) {
		reply(w, 412, `{"error":{"code":"step_up_required","mfa_challenge_token":"mc_1"}}`)
	})
	_, _, code := runCLI(t, "api", "DELETE", "/api/tenants/t1")
	if code != exitIncomplete {
		t.Errorf("exit = %d, want %d (a code is needed and there is no terminal)", code, exitIncomplete)
	}
	if len(f.find("POST", "/auth/mfa/verify")) != 0 {
		t.Errorf("verify was called without a code: %v", f.paths())
	}
}

// Every generated verb on an unconditional step-up route refuses a key session before sending.
func TestGeneratedVerbsOnStepUpRoutesAreNeedsHumanInAKeySession(t *testing.T) {
	f := newBFF(t, nil)
	t.Setenv("REALM_ID_API_KEY", "uk_live_abc")
	cmds, _, err := buildCommands()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, cmd := range cmds {
		if !routeIsUnconditional(cmd.Method, cmd.Path) || mgmtRoute(cmd.Method, cmd.Path) {
			continue
		}
		if len(cmd.Group) == 1 && cmd.Group[0] == "user-api-keys" && cmd.Verb == "update" {
			continue // hand-written verb with its own flags; covered by the § 6.2 tests
		}
		args := append([]string{}, cmd.Group...)
		args = append(args, cmd.Verb)
		for _, p := range cmd.Params {
			args = append(args, "--"+p.Name, "x")
		}
		if cmd.Method != "GET" && cmd.Method != "DELETE" {
			args = append(args, "--json", "{}")
		}
		if _, stderr, code := runCLI(t, args...); code != exitNeedsHuman {
			t.Errorf("%v exit = %d, want %d\n%s", args, code, exitNeedsHuman, stderr)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("examined 0 generated verbs on a step-up route")
	}
	if n := len(f.all()); n != 0 {
		t.Errorf("%d requests sent by refused verbs: %v", n, f.paths())
	}
	t.Logf("examined %d generated verbs on step-up routes", checked)
}

// With REALM_ID_API_KEY set and a person's session saved, the key session is the one credential
// `realm-id api` sends, on a route off the step-up list and on a step-up route alike.
func TestAPIVerbSendsOneCredentialOnEveryRoute(t *testing.T) {
	f := newBFF(t, func(w http.ResponseWriter, r recReq) {
		if r.Path == "/auth/key-session" {
			reply(w, 200, `{"session_token":"rsid_key","session_class":"key"}`)
			return
		}
		reply(w, 200, `{}`)
	})
	t.Setenv("REALM_ID_API_KEY", "uk_live_abc")
	runCLI(t, "api", "GET", "/api/tenants/t1")
	got := f.find("GET", "/api/tenants/t1")
	if len(got) != 1 {
		t.Fatalf("off-list GET requests = %d, want 1: %v", len(got), f.paths())
	}
	if h := got[0].Header.Get("Authorization"); h != "Bearer rsid_key" {
		t.Errorf("off-list route Authorization = %q, want the key session", h)
	}
	if _, _, code := runCLI(t, "api", "DELETE", "/api/tenants/t1"); code != exitNeedsHuman {
		t.Errorf("step-up route in a key session exit = %d, want %d", code, exitNeedsHuman)
	}
	if n := len(f.find("DELETE", "/api/tenants/t1")); n != 0 {
		t.Errorf("step-up route was sent %d times, want 0", n)
	}
}
