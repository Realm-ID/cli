package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"testing"
)

// bffRoutesFile is the reference BFF step-up route list; the test skips when it is absent (as in this repo's own CI).
const bffRoutesFile = "../../../api/internal/stepup/protected_paths.json"

// The CLI's copy of the step-up routes equals the BFF's list, row for row.
func TestStepUpRoutesMatchTheBFFList(t *testing.T) {
	raw, err := os.ReadFile(bffRoutesFile)
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("SKIPPED, NOT VERIFIED: %s is absent (the CLI is not checked out beside the BFF), so the step-up route copy was not compared", bffRoutesFile)
	}
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Policies []struct {
			Method string   `json:"method"`
			Path   string   `json:"path"`
			Field  string   `json:"when_json_field"`
			Values []string `json:"when_json_values"`
		} `json:"policies"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Policies) != len(stepUpRoutes) {
		t.Fatalf("BFF list has %d rows, the CLI copy has %d", len(file.Policies), len(stepUpRoutes))
	}
	for i, p := range file.Policies {
		got := stepUpRoutes[i]
		if got.Method != p.Method || got.Path != p.Path || got.Field != p.Field || !sameStrings(got.Values, p.Values) {
			t.Errorf("row %d: CLI copy %+v, BFF row %+v", i, got, p)
		}
	}
	t.Logf("compared %d rows", len(file.Policies))
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStepUpRequiredReadsTheBodyConditionLikeTheBFF(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		want                     bool
	}{
		{"owner transfer", "PUT", "/tenants/t1/owner", "", true},
		{"query string ignored", "DELETE", "/tenants/t1?x=1", "", true},
		{"method case", "delete", "/tenants/t1", "", true},
		{"path segment count", "DELETE", "/tenants/t1/x", "", false},
		{"status deactivated", "PATCH", "/tenants/t1", `{"status":"deactivated"}`, true},
		{"field name folds case", "PATCH", "/tenants/t1", `{"Status":"deactivated"}`, true},
		{"last duplicate wins", "PATCH", "/tenants/t1", `{"status":"active","STATUS":"deactivated"}`, true},
		{"last duplicate wins, benign last", "PATCH", "/tenants/t1", `{"status":"deactivated","status":"active"}`, false},
		{"other status", "PATCH", "/tenants/t1", `{"status":"active"}`, false},
		{"absent field", "PATCH", "/tenants/t1", `{"name":"x"}`, false},
		{"null field", "PATCH", "/tenants/t1", `{"status":null}`, false},
		{"no body", "PATCH", "/tenants/t1", "", false},
		{"non-string value", "PATCH", "/tenants/t1", `{"status":7}`, true},
		{"malformed body", "PATCH", "/tenants/t1", `{"status":`, true},
		{"star matches any present value", "PATCH", "/tenants/t1/users/u1", `{"email":"a@b.co"}`, true},
		{"star needs the field", "PATCH", "/tenants/t1/users/u1", `{"name":"x"}`, false},
		{"boolean listed", "PATCH", "/platforms/p1/config", `{"email_otp_login_enabled":true}`, true},
		{"boolean not listed value", "PATCH", "/platforms/p1/config", `{"email_otp_login_enabled":false}`, false},
		{"off the list", "GET", "/tenants/t1", "", false},
	}
	for _, c := range cases {
		var body []byte
		if c.body != "" {
			body = []byte(c.body)
		}
		if got := stepUpRequired(c.method, c.path, body); got != c.want {
			t.Errorf("%s: stepUpRequired(%s %s %q) = %v, want %v", c.name, c.method, c.path, c.body, got, c.want)
		}
	}
}
