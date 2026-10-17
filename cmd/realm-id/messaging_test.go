package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const testSecret = "tok_0123456789abcdefghij" // >= 20 chars, as the spec requires

func messagingPut(t *testing.T) command {
	t.Helper()
	cmds, _, err := buildCommands()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cmds {
		if len(c.Group) == 1 && c.Group[0] == "messaging" && c.Verb == "update" {
			return c
		}
	}
	t.Fatal("`messaging update` is not generated (ADR-115 verbs must be exposed)")
	return command{}
}

// stdinFrom swaps os.Stdin for a pipe carrying s (not a TTY, so stdinPiped is true).
func stdinFrom(t *testing.T, s string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString(s)
	_ = w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; _ = r.Close() })
}

// fakeBFF records the body and route of the last request and answers 200.
func fakeBFF(t *testing.T) (gotBody *map[string]any, gotPath *string) {
	t.Helper()
	body := map[string]any{}
	path := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"status":"operational"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("REALM_ID_API_KEY", "")
	t.Setenv("REALM_ID_BFF", srv.URL)
	return &body, &path
}

func TestMessagingVerbsAreGenerated(t *testing.T) {
	cmds, _, err := buildCommands()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range cmds {
		if len(c.Group) == 1 && c.Group[0] == "messaging" {
			got[c.Verb] = c.Method
		}
	}
	want := map[string]string{"describe": "GET", "update": "PUT", "revoke": "DELETE"}
	for v, m := range want {
		if got[v] != m {
			t.Errorf("messaging %s = %q, want %s (got %v)", v, got[v], m, got)
		}
	}
}

func TestMessagingRefusesFlagBorneSecrets(t *testing.T) {
	cmd := messagingPut(t)
	for _, k := range []string{"auth_token", "api_key_secret", "access_token"} {
		for name, pf := range map[string]*parsedFlags{
			"--field":       {vals: map[string]string{"channel": "sms"}, fields: []string{k + "=" + testSecret}},
			"--field typed": {vals: map[string]string{"channel": "sms"}, fields: []string{k + `:="` + testSecret + `"`}},
			"--json":        {vals: map[string]string{"channel": "sms", "json": `{"account_sid":"AC1","` + k + `":"` + testSecret + `"}`}},
		} {
			_, err := buildBody(cmd, pf)
			if err == nil {
				t.Errorf("%s %s: secret accepted, want refusal", name, k)
				continue
			}
			if !strings.Contains(err.Error(), "stdin") {
				t.Errorf("%s %s: error %q must name stdin", name, k, err)
			}
			if strings.Contains(err.Error(), testSecret) {
				t.Errorf("%s %s: error leaks the secret: %q", name, k, err)
			}
		}
	}
}

func TestMessagingStdinJSONBodyReachesServer(t *testing.T) {
	cmd := messagingPut(t)
	body, path := fakeBFF(t)
	stdinFrom(t, `{"account_sid":"AC1","auth_token":"`+testSecret+`","from_number":"+15551234567"}`)
	code := runCommand(&Config{SessionToken: "rsid_s", Platform: "plt1"}, cmd, []string{"--channel", "sms"})
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if *path != "PUT /api/platforms/plt1/messaging/sms" {
		t.Errorf("path = %q", *path)
	}
	if (*body)["auth_token"] != testSecret || (*body)["account_sid"] != "AC1" {
		t.Errorf("body = %v", *body)
	}
}

func TestMessagingSecretStdinFlag(t *testing.T) {
	cmd := messagingPut(t)
	for ch, field := range map[string]string{"sms": "auth_token", "whatsapp": "access_token"} {
		body, _ := fakeBFF(t)
		stdinFrom(t, testSecret+"\r\n")
		code := runCommand(&Config{SessionToken: "rsid_s", Platform: "plt1"}, cmd,
			[]string{"--channel", ch, "--field", "account_sid=AC1", "--secret-stdin"})
		if code != exitOK {
			t.Fatalf("%s: exit %d", ch, code)
		}
		if (*body)[field] != testSecret || (*body)["account_sid"] != "AC1" {
			t.Errorf("%s: body = %v, want %s=secret (trailing CR/LF trimmed) + account_sid", ch, *body, field)
		}
	}
}

func TestMessagingSecretStdinEmptyIsRefused(t *testing.T) {
	cmd := messagingPut(t)
	stdinFrom(t, "\n")
	_, err := buildBody(cmd, &parsedFlags{vals: map[string]string{"channel": "sms", "secret-stdin": "true"}})
	if err == nil {
		t.Fatal("empty --secret-stdin line must be refused")
	}
}

func TestMessagingPromptWhenNoSecretGiven(t *testing.T) {
	cmd := messagingPut(t)
	calls := 0
	secretPrompt = func() (string, error) { calls++; return testSecret, nil }
	t.Cleanup(func() { secretPrompt = nil })
	b, err := buildBody(cmd, &parsedFlags{vals: map[string]string{"channel": "sms"}, fields: []string{"account_sid=AC1"}})
	if err != nil {
		t.Fatal(err)
	}
	m := b.(map[string]any)
	if calls != 1 || m["auth_token"] != testSecret || m["account_sid"] != "AC1" {
		t.Errorf("calls=%d body=%v", calls, m)
	}

	// A secret already supplied (api-key pair via stdin JSON) must not prompt.
	calls = 0
	stdinFrom(t, `{"api_key_sid":"SK1","api_key_secret":"`+testSecret+`"}`)
	if _, err := buildBody(cmd, &parsedFlags{vals: map[string]string{"channel": "sms"}}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("prompted %d times although the body already carried a secret", calls)
	}
}

func TestSecretStdinOnlyForMessaging(t *testing.T) {
	_, err := buildBody(command{HasBody: true, Path: "/things", Method: "POST"},
		&parsedFlags{vals: map[string]string{"secret-stdin": "true"}})
	if err == nil {
		t.Error("--secret-stdin outside messaging must be an error, not silently ignored")
	}
}
