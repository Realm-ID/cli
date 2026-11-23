package main

// The ADR-119 § 6 verbs (platform-keys revoke, --out, tenants set-owner, tenants delete,
// user-api-keys update) and the two command lines partners copy: the setup action's sign-in and the
// platform-key rotation template. Each test drives run() against the fake BFF in v3_helpers_test.go.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const keyBody = `{"id":"key-1","value":"rk_live_secret","scope":"platform","label":"x","expires_at":null}`

// shellLines returns the logical command lines of a workflow/action file that start with prefix:
// backslash continuations joined, ${VAR} and the date substitution expanded from env, quotes
// stripped, and any trailing redirection dropped. It is a tokenizer for the simple lines these
// files hold, not a shell.
func shellLines(t *testing.T, file, prefix string, env map[string]string) [][]string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.ReplaceAll(string(b), "\\\n", " ")
	re := regexp.MustCompile(`\$\{([A-Za-z_]+)\}`)
	var out [][]string
	for _, ln := range strings.Split(raw, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, prefix) {
			continue
		}
		ln = strings.ReplaceAll(ln, "$(date -u +%Y%m%d)", "20261119")
		ln = re.ReplaceAllStringFunc(ln, func(m string) string { return env[re.FindStringSubmatch(m)[1]] })
		var toks []string
		for _, f := range strings.Fields(ln) {
			if f == ">" {
				break
			}
			toks = append(toks, strings.Trim(f, `"`))
		}
		out = append(out, toks[1:]) // drop the program name
	}
	return out
}

func TestSetupActionSignInLineIsAcceptedByTheCLI(t *testing.T) {
	lines := shellLines(t, "../../setup/action.yml", "realm-id auth workload",
		map[string]string{"INPUT_PROVIDER": "github", "INPUT_AUDIENCE": "https://api.realmid.dev"})
	if len(lines) != 1 {
		t.Fatalf("found %d `realm-id auth workload` lines in setup/action.yml, want 1", len(lines))
	}
	f := newBFF(t, nil)
	f.handle = func(w http.ResponseWriter, r recReq) {
		switch r.Path {
		case "/oidc":
			reply(w, 200, `{"value":"gh.oidc.jwt"}`)
		case "/auth/workload-session":
			reply(w, 200, `{"session_token":"rsid_wl","session_class":"workload","role":"platform_mgmt_api"}`)
		default:
			reply(w, 200, `{}`)
		}
	}
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", f.srv.URL+"/oidc?x=1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "gh-req-token")
	ownConfig(t)
	if _, stderr, code := runCLI(t, lines[0]...); code != exitOK {
		t.Fatalf("the action's command line %v exit = %d, want 0\n%s", lines[0], code, stderr)
	}
}

func TestRotationTemplate_CommandLinesRunAndWriteTheKeyFile(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"PLATFORM": v3Realm, "RUNNER_TEMP": dir, "OLD_KEY_ID": "old-9"}
	tpl := "../../setup/examples/rotate-platform-key.yml"
	creates := shellLines(t, tpl, "realm-id platform-keys create", env)
	revokes := shellLines(t, tpl, "realm-id platform-keys revoke", env)
	if len(creates) != 1 || len(revokes) != 1 {
		t.Fatalf("template has %d create and %d revoke lines, want 1 each", len(creates), len(revokes))
	}
	f := newBFF(t, func(w http.ResponseWriter, r recReq) {
		if r.Method == "DELETE" {
			reply(w, 200, `{"status":"revoked"}`)
			return
		}
		reply(w, 201, keyBody)
	})
	stdout, stderr, code := runCLI(t, creates[0]...)
	if code != exitOK {
		t.Fatalf("template create line %v exit = %d\n%s", creates[0], code, stderr)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(stdout), &created); err != nil || created["id"] != "key-1" {
		t.Errorf("stdout = %q, want the key object with .id", stdout)
	}
	file, err := os.ReadFile(filepath.Join(dir, "new-key.json"))
	if err != nil {
		t.Fatalf("the template's --out file was not written: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(file, &onDisk); err != nil || onDisk["value"] != "rk_live_secret" {
		t.Errorf("--out file = %q, want the key under .value", file)
	}
	if _, stderr, code := runCLI(t, revokes[0]...); code != exitOK {
		t.Fatalf("template revoke line %v exit = %d\n%s", revokes[0], code, stderr)
	}
	if got := f.find("DELETE", "/platforms/"+v3Realm+"/api-keys/old-9"); len(got) != 1 {
		t.Errorf("requests = %v, want one DELETE …/api-keys/old-9", f.paths())
	}
}

func TestPlatformKeysRevoke(t *testing.T) {
	path := "/api/platforms/" + v3Realm + "/api-keys/k1"
	t.Run("deletes the key and prints the body", func(t *testing.T) {
		f := newBFF(t, func(w http.ResponseWriter, r recReq) { reply(w, 200, `{"status":"revoked"}`) })
		stdout, stderr, code := runCLI(t, "platform-keys", "revoke", "--realm", v3Realm, "--keyId", "k1")
		if code != exitOK || !strings.Contains(stdout, `"revoked"`) {
			t.Fatalf("exit = %d stdout = %q stderr = %q", code, stdout, stderr)
		}
		if got := f.paths(); len(got) != 1 || got[0] != "DELETE "+path {
			t.Errorf("requests = %v, want DELETE %s", got, path)
		}
	})
	t.Run("answers a step-up with --totp and retries once", func(t *testing.T) {
		n := 0
		f := newBFF(t, func(w http.ResponseWriter, r recReq) {
			switch {
			case r.Path == "/auth/mfa/verify":
				reply(w, 200, `{"session_token":"rsid_new"}`)
			case r.Header.Get("Authorization") != "Bearer rsid_new":
				n++
				reply(w, 412, `{"mfa_challenge_token":"c1"}`)
			default:
				reply(w, 200, `{"status":"revoked"}`)
			}
		})
		if _, stderr, code := runCLI(t, "platform-keys", "revoke", "--realm", v3Realm, "--keyId", "k1", "--totp", "123456"); code != exitOK {
			t.Fatalf("exit = %d\n%s", code, stderr)
		}
		if len(f.find("DELETE", "/api-keys/k1")) != 2 || n != 1 {
			t.Errorf("requests = %v, want the DELETE sent twice around one verify", f.paths())
		}
	})
	t.Run("a key session is needs_human and sends nothing", func(t *testing.T) {
		f := newBFF(t, nil)
		t.Setenv("REALM_ID_API_KEY", "uk_live_abc")
		if _, _, code := runCLI(t, "platform-keys", "revoke", "--realm", v3Realm, "--keyId", "k1"); code != 8 || len(f.all()) != 0 {
			t.Errorf("exit = %d, requests = %v, want 8 and none", code, f.paths())
		}
	})
	t.Run("a platform_mgmt_api workload session may revoke", func(t *testing.T) {
		f := workloadLogin(t, "platform_mgmt_api")
		before := len(f.all())
		if _, stderr, code := runCLI(t, "platform-keys", "revoke", "--realm", v3Realm, "--keyId", "k1"); code != exitOK {
			t.Fatalf("exit = %d\n%s", code, stderr)
		}
		if len(f.find("DELETE", "/api-keys/k1")) != 1 || len(f.all()) != before+1 {
			t.Errorf("requests = %v", f.paths())
		}
	})
	t.Run("another workload actor is needs_human", func(t *testing.T) {
		workloadLogin(t, "member")
		if _, _, code := runCLI(t, "platform-keys", "revoke", "--realm", v3Realm, "--keyId", "k1"); code != 8 {
			t.Errorf("exit = %d, want 8", code)
		}
	})
	t.Run("the issuer's refusals keep their exit codes", func(t *testing.T) {
		for status, want := range map[int]int{404: 5, 409: 4, 403: 7, 503: 1} {
			newBFF(t, func(w http.ResponseWriter, r recReq) { reply(w, status, `{"error":{"code":"x"}}`) })
			if _, _, code := runCLI(t, "platform-keys", "revoke", "--realm", v3Realm, "--keyId", "k1"); code != want {
				t.Errorf("status %d exit = %d, want %d", status, code, want)
			}
		}
	})
	t.Run("--keyId is required and nothing is sent without it", func(t *testing.T) {
		f := newBFF(t, nil)
		if _, _, code := runCLI(t, "platform-keys", "revoke", "--realm", v3Realm); code != exitUsage || len(f.all()) != 0 {
			t.Errorf("exit = %d, requests = %v, want 2 and none", code, f.paths())
		}
	})
}

func TestPlatformKeysCreateOut(t *testing.T) {
	t.Run("writes the whole body 0600 and keeps the value off stdout", func(t *testing.T) {
		newBFF(t, func(w http.ResponseWriter, r recReq) { reply(w, 201, keyBody) })
		out := filepath.Join(t.TempDir(), "key.json")
		stdout, stderr, code := runCLI(t, "platform-keys", "create", "--realm", v3Realm, "--label", "x", "--out", out)
		if code != exitOK {
			t.Fatalf("exit = %d\n%s", code, stderr)
		}
		st, err := os.Stat(out)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("mode = %o, want 600", st.Mode().Perm())
		}
		b, _ := os.ReadFile(out)
		var onDisk map[string]any
		if err := json.Unmarshal(b, &onDisk); err != nil || onDisk["value"] != "rk_live_secret" || onDisk["id"] != "key-1" {
			t.Errorf("file = %q, want the whole body", b)
		}
		if strings.Contains(stdout+stderr, "rk_live_secret") {
			t.Errorf("the key value reached stdout or stderr: %q %q", stdout, stderr)
		}
		var o map[string]any
		if err := json.Unmarshal([]byte(stdout), &o); err != nil || o["id"] != "key-1" || o["written_to"] != out {
			t.Errorf("stdout = %q, want the object without value plus written_to", stdout)
		}
		if _, has := o["value"]; has {
			t.Errorf("stdout still carries value")
		}
	})
	t.Run("an existing path is exit 2 and nothing is sent", func(t *testing.T) {
		f := newBFF(t, nil)
		out := filepath.Join(t.TempDir(), "key.json")
		if err := os.WriteFile(out, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, code := runCLI(t, "platform-keys", "create", "--realm", v3Realm, "--label", "x", "--out", out); code != exitUsage || len(f.all()) != 0 {
			t.Errorf("exit = %d, requests = %v, want 2 and none", code, f.paths())
		}
		if b, _ := os.ReadFile(out); string(b) != "keep" {
			t.Errorf("the existing file was touched: %q", b)
		}
	})
	t.Run("a refused mint leaves no file", func(t *testing.T) {
		newBFF(t, func(w http.ResponseWriter, r recReq) { reply(w, 409, `{"error":{"code":"too_many_api_keys"}}`) })
		out := filepath.Join(t.TempDir(), "key.json")
		if _, _, code := runCLI(t, "platform-keys", "create", "--realm", v3Realm, "--label", "x", "--out", out); code != 4 {
			t.Errorf("exit = %d, want 4", code)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Errorf("the empty file was left behind (stat err = %v)", err)
		}
	})
	t.Run("without --out the body is unchanged", func(t *testing.T) {
		newBFF(t, func(w http.ResponseWriter, r recReq) { reply(w, 201, keyBody) })
		stdout, _, code := runCLI(t, "platform-keys", "create", "--realm", v3Realm, "--label", "x")
		if code != exitOK || !strings.Contains(stdout, "rk_live_secret") {
			t.Errorf("exit = %d stdout = %q, want the full body", code, stdout)
		}
	})
}

func TestTenantsSetOwnerAndDelete(t *testing.T) {
	t.Run("set-owner puts the body", func(t *testing.T) {
		f := newBFF(t, func(w http.ResponseWriter, r recReq) { reply(w, 200, `{"ok":true}`) })
		_, stderr, code := runCLI(t, "tenants", "set-owner", "--tenant", "t1", "--field", "new_owner_email=a@b.co", "--field", "leave_entirely:=true")
		if code != exitOK {
			t.Fatalf("exit = %d\n%s", code, stderr)
		}
		got := f.find("PUT", "/tenants/t1/owner")
		if len(got) != 1 || !strings.Contains(string(got[0].Body), `"new_owner_email":"a@b.co"`) || !strings.Contains(string(got[0].Body), `"leave_entirely":true`) {
			t.Errorf("requests = %v body = %v", f.paths(), got)
		}
	})
	t.Run("delete is a DELETE on the tenant", func(t *testing.T) {
		f := newBFF(t, func(w http.ResponseWriter, r recReq) { reply(w, 200, `{"status":"deleted"}`) })
		if _, stderr, code := runCLI(t, "tenants", "delete", "--tenant", "t1"); code != exitOK {
			t.Fatalf("exit = %d\n%s", code, stderr)
		}
		if got := f.paths(); len(got) != 1 || got[0] != "DELETE /api/tenants/t1" {
			t.Errorf("requests = %v", got)
		}
	})
	t.Run("both answer a step-up with --totp", func(t *testing.T) {
		for _, args := range [][]string{
			{"tenants", "delete", "--tenant", "t1", "--totp", "123456"},
			{"tenants", "set-owner", "--tenant", "t1", "--field", "new_owner_email=a@b.co", "--totp", "123456"},
		} {
			f := newBFF(t, func(w http.ResponseWriter, r recReq) {
				switch {
				case r.Path == "/auth/mfa/verify":
					reply(w, 200, `{"session_token":"rsid_new"}`)
				case r.Header.Get("Authorization") != "Bearer rsid_new":
					reply(w, 412, `{"mfa_challenge_token":"c1"}`)
				default:
					reply(w, 200, `{}`)
				}
			})
			if _, stderr, code := runCLI(t, args...); code != exitOK {
				t.Fatalf("%v exit = %d\n%s", args[:2], code, stderr)
			}
			if len(f.find("POST", "/auth/mfa/verify")) != 1 {
				t.Errorf("%v requests = %v, want one verify", args[:2], f.paths())
			}
		}
	})
	t.Run("a key or workload session is needs_human and sends nothing", func(t *testing.T) {
		for _, args := range [][]string{
			{"tenants", "delete", "--tenant", "t1"},
			{"tenants", "set-owner", "--tenant", "t1", "--field", "new_owner_email=a@b.co"},
		} {
			f := newBFF(t, nil)
			t.Setenv("REALM_ID_API_KEY", "uk_live_abc")
			if _, _, code := runCLI(t, args...); code != 8 || len(f.all()) != 0 {
				t.Errorf("%v exit = %d, requests = %v, want 8 and none", args[:2], code, f.paths())
			}
		}
		f := workloadLogin(t, "platform_mgmt_api") // R72 covers platform-keys only
		before := len(f.all())
		if _, _, code := runCLI(t, "tenants", "delete", "--tenant", "t1"); code != 8 || len(f.all()) != before {
			t.Errorf("mgmt actor tenants delete exit = %d, want 8 with nothing sent", code)
		}
	})
}

func TestUserAPIKeysUpdate(t *testing.T) {
	const path = "/api/tenants/t1/users/u1/user-api-keys/k1"
	t.Run("puts label, uncapped and permissions_cap", func(t *testing.T) {
		f := newBFF(t, func(w http.ResponseWriter, r recReq) { reply(w, 200, `{"id":"k1","label":"new"}`) })
		stdout, stderr, code := runCLI(t, "user-api-keys", "update", "--tenant", "t1", "--user", "u1", "--id", "k1",
			"--label", "new", "--permissions", "a:b,c:d", "--ttl-seconds", "60")
		if code != exitOK || !strings.Contains(stdout, `"k1"`) {
			t.Fatalf("exit = %d stdout = %q stderr = %q", code, stdout, stderr)
		}
		reqs := f.find("PUT", "/user-api-keys/k1")
		if len(reqs) != 1 || reqs[0].Path != path {
			t.Fatalf("requests = %v, want PUT %s", f.paths(), path)
		}
		var body map[string]any
		_ = json.Unmarshal(reqs[0].Body, &body)
		caps, _ := body["permissions_cap"].([]any)
		if body["label"] != "new" || body["uncapped"] != false || len(caps) != 2 || body["ttl_seconds"] != float64(60) {
			t.Errorf("body = %s", reqs[0].Body)
		}
	})
	t.Run("authority must be stated", func(t *testing.T) {
		f := newBFF(t, nil)
		if _, _, code := runCLI(t, "user-api-keys", "update", "--tenant", "t1", "--user", "u1", "--id", "k1", "--label", "new"); code != exitUsage || len(f.all()) != 0 {
			t.Errorf("exit = %d, requests = %v, want 2 and none", code, f.paths())
		}
	})
	t.Run("--uncapped sends uncapped true", func(t *testing.T) {
		f := newBFF(t, nil)
		if _, _, code := runCLI(t, "user-api-keys", "update", "--tenant", "t1", "--user", "u1", "--id", "k1", "--label", "n", "--uncapped"); code != exitOK {
			t.Fatalf("exit = %d", code)
		}
		if b := f.find("PUT", "/k1")[0].Body; !strings.Contains(string(b), `"uncapped":true`) {
			t.Errorf("body = %s", b)
		}
	})
	t.Run("409 is exit 4, 404 is exit 5", func(t *testing.T) {
		for status, want := range map[int]int{409: 4, 404: 5} {
			newBFF(t, func(w http.ResponseWriter, r recReq) { reply(w, status, `{"error":{"code":"x"}}`) })
			if _, _, code := runCLI(t, "user-api-keys", "update", "--tenant", "t1", "--user", "u1", "--id", "k1", "--label", "n", "--uncapped"); code != want {
				t.Errorf("status %d exit = %d, want %d", status, code, want)
			}
		}
	})
	t.Run("a key session is needs_human and sends nothing", func(t *testing.T) {
		f := newBFF(t, nil)
		t.Setenv("REALM_ID_API_KEY", "uk_live_abc")
		if _, _, code := runCLI(t, "user-api-keys", "update", "--tenant", "t1", "--user", "u1", "--id", "k1", "--label", "n", "--uncapped"); code != 8 || len(f.all()) != 0 {
			t.Errorf("exit = %d, requests = %v, want 8 and none", code, f.paths())
		}
	})
}
