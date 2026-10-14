package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type planOut struct {
	OK       bool `json:"ok"`
	Rows     int  `json:"rows"`
	Problems []struct {
		Row      int    `json:"row"`
		FileLine int    `json:"file_line"`
		Field    string `json:"field"`
		Code     string `json:"code"`
		Severity string `json:"severity"`
		Message  string `json:"message"`
	} `json:"problems"`
	Summary struct {
		Errors   int `json:"errors"`
		Warnings int `json:"warnings"`
		BadRows  int `json:"rows_with_errors"`
	} `json:"summary"`
}

func runPlan(t *testing.T, csv string) (planOut, int) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.csv")
	if err := os.WriteFile(p, []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := captureStd(t, func() int { return run([]string{"plan-import", "--output", "json", p}) })
	var o planOut
	if err := json.Unmarshal([]byte(stdout), &o); err != nil {
		t.Fatalf("stdout not JSON (%v): %q stderr=%q", err, stdout, stderr)
	}
	return o, code
}

func (o planOut) has(row int, code string) bool {
	for _, p := range o.Problems {
		if p.Row == row && p.Code == code {
			return true
		}
	}
	return false
}

const goodUUID = "0198aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee"

func TestPlanImport_CleanFile(t *testing.T) {
	o, code := runPlan(t, "user_id,email,phone,role,display_name,provider,provider_uid,created_at\n"+
		goodUUID+",a@x.com,+14155550101,member,Ann,google,g-1,2024-01-02T03:04:05Z\n"+
		",b@x.com,,admin,Bob,,,\n")
	if code != exitOK || !o.OK || o.Rows != 2 || o.Summary.Errors != 0 {
		t.Fatalf("clean file must pass: code=%d %+v", code, o)
	}
}

func TestPlanImport_ReportsEveryProblemWithRowNumbers(t *testing.T) {
	csv := "user_id,email,phone,role,provider,provider_uid,created_at\n" +
		"not-a-uuid,a@x.com,,member,,,\n" + //                           row 1 invalid_user_id
		",A@X.com,,member,,,\n" + //                                       row 2 duplicate email (row 1, case-insens)
		",no-at-sign,,member,,,\n" + //                                    row 3 invalid_email
		",,,member,,,\n" + //                                              row 4 missing_identifier
		",c@x.com,,,,,\n" + //                                             row 5 missing_role
		",d@x.com,,owner,,,\n" + //                                        row 6 forbidden_role
		",e@x.com,+1 (555) 010-9999,member,,,\n" + //                      row 7 invalid_phone
		",f@x.com,,member,google,,\n" + //                                 row 8 incomplete_provider
		",g@x.com,,member,github,uid,\n" + //                              row 9 unknown_provider
		",h@x.com,,member,,,yesterday\n" + //                              row 10 invalid_created_at
		goodUUID + ",i@x.com,,member,,,\n" + //                            row 11 ok
		goodUUID + ",j@x.com,,member,,,\n" //                              row 12 duplicate user_id (row 11)
	o, code := runPlan(t, csv)
	if code != exitErr || o.OK {
		t.Fatalf("must fail: code=%d ok=%v", code, o.OK)
	}
	want := map[int]string{
		1: "invalid_user_id", 2: "duplicate_in_batch", 3: "invalid_email", 4: "missing_identifier",
		5: "missing_role", 6: "forbidden_role", 7: "invalid_phone", 8: "incomplete_provider",
		9: "unknown_provider", 10: "invalid_created_at", 12: "duplicate_in_batch",
	}
	for row, c := range want {
		if !o.has(row, c) {
			t.Errorf("row %d: missing %s; problems=%+v", row, c, o.Problems)
		}
	}
	if o.has(11, "invalid_user_id") || o.Summary.BadRows != len(want) {
		t.Errorf("row 11 is fine and bad rows should be %d, got %d", len(want), o.Summary.BadRows)
	}
	// the physical file line (header is line 1) is reported beside the row.
	for _, p := range o.Problems {
		if p.FileLine != p.Row+1 {
			t.Errorf("row %d should be file line %d, got %d", p.Row, p.Row+1, p.FileLine)
		}
	}
}

func TestPlanImport_FileLevelProblems(t *testing.T) {
	// no role column, no identifier column
	if o, code := runPlan(t, "display_name\nAnn\n"); code != exitErr || !o.has(0, "missing_column") {
		t.Errorf("missing columns must be a file-level (row 0) error: code=%d %+v", code, o.Problems)
	}
	// header only
	if o, code := runPlan(t, "email,role\n"); code != exitErr || !o.has(0, "empty_import") {
		t.Errorf("no data rows is the issuer's empty_import: code=%d %+v", code, o.Problems)
	}
	// unknown column is a warning, not an error: the issuer ignores it.
	o, code := runPlan(t, "email,role,shoe_size\na@x.com,member,42\n")
	if code != exitOK || o.Summary.Warnings != 1 {
		t.Errorf("unknown column: want pass with 1 warning, got code=%d %+v", code, o)
	}
	// over the 1000-row cap
	var sb strings.Builder
	sb.WriteString("email,role\n")
	for i := 0; i < 1001; i++ {
		sb.WriteString("u" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + itoa(i) + "@x.com,member\n")
	}
	if o, code := runPlan(t, sb.String()); code != exitErr || !o.has(0, "too_many_rows") {
		t.Errorf("1001 rows must trip too_many_rows: code=%d", code)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestPlanImport_NeverCallsTheAPI(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	t.Setenv("REALM_ID_BFF", srv.URL)
	t.Setenv("REALM_ID_ISSUER", srv.URL)
	runPlan(t, "email,role\na@x.com,member\n")
	if hits != 0 {
		t.Fatalf("plan-import made %d HTTP calls; it is offline by contract", hits)
	}
}

func TestPlanImport_Usage(t *testing.T) {
	if run([]string{"plan-import"}) != exitUsage {
		t.Fatal("no file should be a usage error")
	}
	if run([]string{"plan-import", "/no/such/file.csv"}) == exitOK {
		t.Fatal("unreadable file must fail")
	}
}
