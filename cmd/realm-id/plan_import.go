package main

// `realm-id plan-import <csv>` — validate a bulk user-import file OFFLINE,
// before anything is written. It never touches the network.
//
// The rules are derived from the issuer's handler, not from the spec text
// (issuer internal/httpapi/user_import.go, gImportUsers/importUsersCore phase 1),
// because the handler is what actually accepts or rejects a file:
//   - columns are the ImportUserRow fields: user_id, email, phone, role,
//     display_name, provider, provider_uid, created_at;
//   - role is required; at least one of email/phone per row;
//   - email: the handler only requires an "@" (lowercased, trimmed);
//   - phone: STRICT E.164 (contact.IsE164, ADR-103 D5) — the swagger text still
//     says only "leading '+'", which is stale;
//   - provider and provider_uid both or neither; provider in
//     google|microsoft|apple|facebook|firebase;
//   - created_at RFC3339; user_id a UUID; email, phone and user_id unique in
//     the file; owner/platform_api roles refused; at most 1000 rows; no rows =
//     empty_import.
//
// What it CANNOT know offline (and says so): whether a role exists in the
// realm, whether the actor may seat it, identifier or user_id collisions with
// existing data, and single_tenant_membership. Those come back from the issuer
// in the phase-1 report of the real call, which writes nothing on failure.
//
// "row" is the issuer's own `line` (1-based index of the data row, header
// excluded) so a problem here matches the issuer's report; "file_line" is the
// physical line for finding it in an editor.

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

const planImportMaxRows = 1000 // issuer importMaxRows

var planColumns = []string{"user_id", "email", "phone", "role", "display_name", "provider", "provider_uid", "created_at"}

var planProviders = map[string]bool{"google": true, "microsoft": true, "apple": true, "facebook": true, "firebase": true}

type planProblem struct {
	Row      int    `json:"row"`
	FileLine int    `json:"file_line"`
	Field    string `json:"field,omitempty"`
	Code     string `json:"code"`
	Severity string `json:"severity"` // error | warning
	Message  string `json:"message"`
}

type planSummary struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	BadRows  int `json:"rows_with_errors"`
}

type planResult struct {
	OK         bool          `json:"ok"`
	Rows       int           `json:"rows"`
	Problems   []planProblem `json:"problems"`
	Summary    planSummary   `json:"summary"`
	NotChecked []string      `json:"not_checked_offline"`
}

var planNotChecked = []string{
	"role exists in the realm and the actor may seat it (unknown_role, forbidden_role for admin roles)",
	"identifier or user_id already in use (identifier_collision, cross_tenant_user_id)",
	"single_tenant_membership (realm allows one org per user)",
}

var uuidCanon = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var hex32 = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// normUUID returns a canonical lowercase no-dash form, or "" if s is not a
// UUID in any form github.com/google/uuid (the issuer's parser) accepts.
func normUUID(s string) string {
	s = strings.TrimPrefix(s, "urn:uuid:")
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		s = s[1 : len(s)-1]
	}
	if uuidCanon.MatchString(s) {
		return strings.ToLower(strings.ReplaceAll(s, "-", ""))
	}
	if hex32.MatchString(s) {
		return strings.ToLower(s)
	}
	return ""
}

func cmdPlanImport(args []string) int {
	h, err := parseHelperArgs(args, "output")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUsage
	}
	if len(h.pos) != 1 {
		fmt.Fprintln(os.Stderr, "usage: realm-id plan-import [--output json|text] <file.csv|->")
		return exitUsage
	}
	var in io.Reader = os.Stdin
	if h.pos[0] != "-" {
		f, oerr := os.Open(h.pos[0])
		if oerr != nil {
			return fail(oerr)
		}
		defer f.Close()
		in = f
	}
	res, perr := planImport(in)
	if perr != nil {
		return fail(perr)
	}
	if wantJSON(h.flags["output"]) {
		writeJSON(res)
	} else {
		printPlanText(res)
	}
	if !res.OK {
		return exitErr
	}
	return exitOK
}

func printPlanText(r planResult) {
	w := os.Stdout
	for _, p := range r.Problems {
		where := "file"
		if p.Row > 0 {
			where = fmt.Sprintf("row %d (line %d)", p.Row, p.FileLine)
		}
		fmt.Fprintf(w, "%-7s %s: %s: %s\n", strings.ToUpper(p.Severity), where, p.Code, p.Message)
	}
	verdict := "OK"
	if !r.OK {
		verdict = "NOT OK"
	}
	fmt.Fprintf(w, "%s: %d rows, %d errors in %d rows, %d warnings\n",
		verdict, r.Rows, r.Summary.Errors, r.Summary.BadRows, r.Summary.Warnings)
	fmt.Fprintln(w, "not checked offline: "+strings.Join(r.NotChecked, "; "))
}

// planImport reads the CSV and returns the report; the error is for an
// unreadable/unparseable CSV container (bad quoting), not for bad content.
func planImport(in io.Reader) (planResult, error) {
	res := planResult{Problems: []planProblem{}, NotChecked: planNotChecked}
	badRows := map[int]bool{}
	add := func(row, line int, field, code, sev, msg string) {
		res.Problems = append(res.Problems, planProblem{Row: row, FileLine: line, Field: field, Code: code, Severity: sev, Message: msg})
		if sev == "error" {
			res.Summary.Errors++
			if row > 0 {
				badRows[row] = true
			}
		} else {
			res.Summary.Warnings++
		}
	}

	r := csv.NewReader(in)
	r.FieldsPerRecord = -1
	r.ReuseRecord = false
	header, err := r.Read()
	if err == io.EOF {
		add(0, 1, "", "empty_import", "error", "the file is empty: no header and no users to import")
		return finishPlan(res, badRows), nil
	}
	if err != nil {
		return res, fmt.Errorf("reading CSV header: %w", err)
	}
	col := map[string]int{}
	for i, name := range header {
		name = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, string(rune(0xFEFF)))))
		if name == "" {
			continue
		}
		known := false
		for _, k := range planColumns {
			known = known || k == name
		}
		if _, dup := col[name]; dup {
			add(0, 1, name, "duplicate_column", "error", "column "+name+" appears twice in the header")
			continue
		}
		col[name] = i
		if !known {
			add(0, 1, name, "unknown_column", "warning", "column "+name+" is not an import field; the issuer ignores it (it answers with a 'Warning: 299 ignored unknown fields' header)")
		}
	}
	if _, ok := col["role"]; !ok {
		add(0, 1, "role", "missing_column", "error", "no role column; every row needs a role")
	}
	_, hasEmail := col["email"]
	_, hasPhone := col["phone"]
	if !hasEmail && !hasPhone {
		add(0, 1, "email", "missing_column", "error", "no email or phone column; every row needs at least one")
	}

	seenEmail, seenPhone, seenUID := map[string]int{}, map[string]int{}, map[string]int{}
	row := 0
	for {
		rec, rerr := r.Read()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return res, fmt.Errorf("reading CSV row %d: %w", row+1, rerr)
		}
		row++
		line, _ := r.FieldPos(0)
		get := func(name string) string {
			if i, ok := col[name]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		bad := func(field, code, msg string) { add(row, line, field, code, "error", msg) }

		email := strings.ToLower(get("email"))
		phone := get("phone")
		have := 0
		if email != "" {
			if !strings.Contains(email, "@") {
				bad("email", "invalid_email", "email must contain '@'")
			} else {
				have++
				if prev, dup := seenEmail[email]; dup {
					bad("email", "duplicate_in_batch", fmt.Sprintf("email also appears on row %d", prev))
				} else {
					seenEmail[email] = row
				}
				if !looksLikeEmail(email) {
					add(row, line, "email", "suspicious_email", "warning", "the issuer accepts this (it only needs '@') but it does not look like name@domain.tld")
				}
			}
		}
		if phone != "" {
			if !isE164(phone) {
				bad("phone", "invalid_phone", "phone must be canonical E.164: '+', a non-zero country digit, 8-15 digits total, no spaces or punctuation")
			} else {
				have++
				if prev, dup := seenPhone[phone]; dup {
					bad("phone", "duplicate_in_batch", fmt.Sprintf("phone also appears on row %d", prev))
				} else {
					seenPhone[phone] = row
				}
			}
		}
		if have == 0 && email == "" && phone == "" {
			bad("email", "missing_identifier", "each row needs an email or phone")
		}

		switch role := get("role"); role {
		case "":
			bad("role", "missing_role", "role is required")
		case "owner", "platform_api":
			bad("role", "forbidden_role", "owner and platform_api cannot be assigned by import")
		}

		prov, puid := strings.ToLower(get("provider")), get("provider_uid")
		if (prov == "") != (puid == "") {
			bad("provider", "incomplete_provider", "provider and provider_uid must be supplied together")
		} else if prov != "" && !planProviders[prov] {
			bad("provider", "unknown_provider", "provider must be one of google, microsoft, apple, facebook, firebase")
		}

		if ca := get("created_at"); ca != "" {
			if _, perr := time.Parse(time.RFC3339, ca); perr != nil {
				bad("created_at", "invalid_created_at", "created_at must be an RFC3339 timestamp, e.g. 2024-01-02T03:04:05Z")
			}
		}

		if uid := get("user_id"); uid != "" {
			n := normUUID(uid)
			if n == "" {
				bad("user_id", "invalid_user_id", "user_id must be a UUID")
			} else if prev, dup := seenUID[n]; dup {
				bad("user_id", "duplicate_in_batch", fmt.Sprintf("user_id also appears on row %d", prev))
			} else {
				seenUID[n] = row
			}
		}
	}
	res.Rows = row
	if row == 0 {
		add(0, 1, "", "empty_import", "error", "no users to import (header only)")
	}
	if row > planImportMaxRows {
		add(0, 1, "", "too_many_rows", "error", fmt.Sprintf("%d rows exceeds the issuer's per-request cap of %d; split the file into batches", row, planImportMaxRows))
	}
	return finishPlan(res, badRows), nil
}

func finishPlan(res planResult, badRows map[int]bool) planResult {
	res.Summary.BadRows = len(badRows)
	res.OK = res.Summary.Errors == 0
	return res
}

// isE164 mirrors the issuer's contact.IsE164 shape check exactly.
func isE164(s string) bool {
	if len(s) < 9 || len(s) > 16 || s[0] != '+' || s[1] < '1' || s[1] > '9' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

var emailShape = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func looksLikeEmail(s string) bool { return emailShape.MatchString(s) }
