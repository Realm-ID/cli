package main

// `realm-id auth workload` and the config it writes: a person's saved session is never replaced,
// and the OIDC audience defaults to the BFF.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oidcBFF is a fake BFF that also serves the runner's OIDC token endpoint at /oidc and records the
// audience it was asked for (gotAudience is "<unset>" when none was requested).
func oidcBFF(t *testing.T, gotAudience *string) *v3BFF {
	t.Helper()
	f := newBFF(t, nil)
	f.handle = func(w http.ResponseWriter, r recReq) {
		switch r.Path {
		case "/oidc":
			if gotAudience != nil {
				*gotAudience = "<unset>"
				if v := r.Query.Get("audience"); v != "" {
					*gotAudience = v
				}
			}
			reply(w, 200, `{"value":"gh.oidc.jwt"}`)
		case "/auth/workload-session":
			reply(w, 200, `{"session_token":"rsid_wl","session_class":"workload","role":"member"}`)
		default:
			reply(w, 200, `{}`)
		}
	}
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", f.srv.URL+"/oidc?x=1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "gh-req-token")
	return f
}

// ownConfig points the CLI at a config file of its own, with no person's session in it (the
// workload job's file).
func ownConfig(t *testing.T) {
	t.Helper()
	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "job", "config.json"))
}

func TestAuthWorkloadRefusesWhenTheConfigHoldsAPersonsSession(t *testing.T) {
	f := oidcBFF(t, nil) // newBFF seeds the config with a person's session (rsid_human)
	cfgPath := os.Getenv("REALM_ID_CONFIG")
	before, _ := os.ReadFile(cfgPath)
	_, stderr, code := runCLI(t, "auth", "workload")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d (a person's session is saved in this config)\n%s", code, exitUsage, stderr)
	}
	after, _ := os.ReadFile(cfgPath)
	if string(after) != string(before) {
		t.Errorf("config changed:\nbefore %s\nafter  %s", before, after)
	}
	if n := len(f.all()); n != 0 {
		t.Errorf("%d requests sent, want none: %v", n, f.paths())
	}
}

func TestAuthWorkloadStoresItsSessionInAFreshConfigAt0600(t *testing.T) {
	f := oidcBFF(t, nil)
	cfgPath := filepath.Join(t.TempDir(), "job", "config.json")
	t.Setenv("REALM_ID_CONFIG", cfgPath)
	if _, stderr, code := runCLI(t, "auth", "workload"); code != exitOK {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	st, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	b, _ := os.ReadFile(cfgPath)
	var c map[string]string
	_ = json.Unmarshal(b, &c)
	if c["session_token"] != "rsid_wl" || c["session_class"] != "workload" {
		t.Errorf("config = %s, want the workload session", b)
	}
	if len(f.find("POST", "/auth/workload-session")) != 1 {
		t.Errorf("requests = %v", f.paths())
	}
}

func TestAuthWorkloadReplacesAnEarlierWorkloadSession(t *testing.T) {
	oidcBFF(t, nil)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"session_token":"rsid_old","session_class":"workload","session_role":"member"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REALM_ID_CONFIG", cfgPath)
	if _, stderr, code := runCLI(t, "auth", "workload"); code != exitOK {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	if b, _ := os.ReadFile(cfgPath); !strings.Contains(string(b), "rsid_wl") {
		t.Errorf("config = %s, want the new workload session", b)
	}
}

func TestAuthWorkloadAudienceDefaultsToTheBFF(t *testing.T) {
	var aud string
	f := oidcBFF(t, &aud)
	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	if _, stderr, code := runCLI(t, "auth", "workload"); code != exitOK {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	if aud != f.srv.URL {
		t.Errorf("audience requested = %q, want the BFF %q", aud, f.srv.URL)
	}
}

func TestSetupActionKeepsTheJobSessionInItsOwnConfigFile(t *testing.T) {
	b, err := os.ReadFile("../../setup/action.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	exp := strings.Index(s, `export REALM_ID_CONFIG="${REALM_ID_CONFIG:-${RUNNER_TEMP}/`)
	run := strings.Index(s, "realm-id auth workload --audience")
	if exp < 0 || run < 0 || exp > run {
		t.Errorf("setup/action.yml must export REALM_ID_CONFIG under RUNNER_TEMP before `realm-id auth workload` (export at %d, sign-in at %d)", exp, run)
	}
	if !strings.Contains(s, `>> "${GITHUB_ENV}"`) {
		t.Error("setup/action.yml must carry REALM_ID_CONFIG to later steps through GITHUB_ENV")
	}
}

func TestAuthWorkloadAudienceFlagWins(t *testing.T) {
	var aud string
	oidcBFF(t, &aud)
	t.Setenv("REALM_ID_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	want := "https://other.example"
	if _, stderr, code := runCLI(t, "auth", "workload", "--audience", want); code != exitOK {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	if u, _ := url.QueryUnescape(aud); u != want {
		t.Errorf("audience requested = %q, want %q", aud, want)
	}
}
