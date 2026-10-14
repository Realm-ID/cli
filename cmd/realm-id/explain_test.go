package main

import (
	"encoding/json"
	"strings"
	"testing"
)

type explainOut struct {
	Command string `json:"command"`
	OK      bool   `json:"ok"`
	Errors  []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Code        string   `json:"code"`
	SourceSHA   string   `json:"source_sha"`
	Suggestions []string `json:"suggestions"`
	Matches     []struct {
		Section  string `json:"section"`
		When     string `json:"when"`
		Meaning  string `json:"meaning"`
		WhatToDo string `json:"what_to_do"`
	} `json:"matches"`
}

func runExplain(t *testing.T, args ...string) (explainOut, int) {
	t.Helper()
	stdout, stderr, code := captureStd(t, func() int { return run(append([]string{"explain"}, args...)) })
	var o explainOut
	if err := json.Unmarshal([]byte(stdout), &o); err != nil {
		t.Fatalf("stdout not JSON (%v): %q stderr=%q", err, stdout, stderr)
	}
	return o, code
}

func TestExplain_PositiveControl(t *testing.T) {
	o, code := runExplain(t, "--output", "json", "missing_tenant_id")
	if code != exitOK || !o.OK || o.Command != "explain" || o.Code != "missing_tenant_id" {
		t.Fatalf("code=%d out=%+v", code, o)
	}
	if len(o.Matches) == 0 || o.Matches[0].Section == "" || o.Matches[0].WhatToDo == "" {
		t.Fatalf("no usable row: %+v", o.Matches)
	}
	if len(o.SourceSHA) != 40 {
		t.Fatalf("source_sha %q", o.SourceSHA)
	}
}

func TestExplain_MeaningHeaderShape(t *testing.T) {
	rows := parseErrorReference(errorReferenceMD)
	var want errorRow
	for _, r := range rows {
		if r.Meaning != "" {
			want = r
			break
		}
	}
	if want.Code == "" {
		t.Fatal("no row from a `Code | Meaning | What to do` table was parsed")
	}
	o, code := runExplain(t, "--output", "json", want.Code)
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	found := false
	for _, m := range o.Matches {
		if m.Meaning == want.Meaning && m.When == "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("meaning row not returned: %+v", o.Matches)
	}
}

func TestParse_BacktickPipeCell(t *testing.T) {
	md := "## Sec A\n\n| Code | When | What to do |\n| --- | --- | --- |\n" +
		"| `a_code` | Value is `x|y` or `z`. | Use `p|q` instead. |\n\n" +
		"## Sec B\n\n| Code | Meaning | What to do |\n|---|---|---|\n| `a_code` | Second. | Other. |\n"
	rows := parseErrorReference([]byte(md))
	if len(rows) != 2 {
		t.Fatalf("rows=%+v", rows)
	}
	if rows[0].Code != "a_code" || rows[0].When != "Value is `x|y` or `z`." || rows[0].WhatToDo != "Use `p|q` instead." || rows[0].Section != "Sec A" {
		t.Fatalf("row0=%+v", rows[0])
	}
	if rows[1].Section != "Sec B" || rows[1].Meaning != "Second." || rows[1].When != "" {
		t.Fatalf("row1=%+v", rows[1])
	}
}

func TestExplain_CellWithTwoCodes(t *testing.T) {
	for _, c := range []string{"password_too_short", "password_too_long"} {
		if o, code := runExplain(t, "--output", "json", c); code != exitOK || len(o.Matches) == 0 {
			t.Fatalf("%s: code=%d %+v", c, code, o)
		}
	}
}

func TestExplain_UnknownCodeSuggests(t *testing.T) {
	o, code := runExplain(t, "--output", "json", "missing_tenant_i")
	if code != exitErr || o.OK {
		t.Fatalf("code=%d ok=%v", code, o.OK)
	}
	if len(o.Errors) != 1 || o.Errors[0].Code != "unknown_error_code" {
		t.Fatalf("errors=%+v", o.Errors)
	}
	if len(o.Suggestions) == 0 || o.Suggestions[0] != "missing_tenant_id" {
		t.Fatalf("suggestions=%v", o.Suggestions)
	}
	o, code = runExplain(t, "--output", "json", "zzzzzzzzzzzzzzzzzzzz")
	if code != exitErr || len(o.Suggestions) != 0 {
		t.Fatalf("garbage should have no suggestions: %v", o.Suggestions)
	}
}

func TestExplain_Text(t *testing.T) {
	stdout, _, code := captureStd(t, func() int { return run([]string{"explain", "--output", "text", "missing_tenant_id"}) })
	if code != exitOK || !strings.Contains(stdout, "missing_tenant_id") || !strings.Contains(stdout, "What to do") {
		t.Fatalf("code=%d out=%q", code, stdout)
	}
}

func TestExplain_Usage(t *testing.T) {
	if run([]string{"explain"}) != exitUsage {
		t.Fatal("no code should be a usage error")
	}
	if run([]string{"explain", "a", "b"}) != exitUsage {
		t.Fatal("two codes should be a usage error")
	}
	if run([]string{"explain", "--nope", "a"}) != exitUsage {
		t.Fatal("unknown flag should be a usage error")
	}
}

func TestErrorReference_ParseIsSane(t *testing.T) {
	rows := parseErrorReference(errorReferenceMD)
	if len(rows) < 200 {
		t.Fatalf("parsed only %d rows; the parser is broken or the table shape changed", len(rows))
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Code == "" || r.Section == "" || r.WhatToDo == "" || (r.When == "" && r.Meaning == "") {
			t.Errorf("empty field in %+v", r)
		}
		if strings.Contains(r.Code, "`") || strings.ContainsAny(r.Code, " |") {
			t.Errorf("code not clean: %q", r.Code)
		}
		k := r.Section + "\x00" + r.Code
		if seen[k] {
			t.Errorf("duplicate code %q in section %q", r.Code, r.Section)
		}
		seen[k] = true
	}
}
