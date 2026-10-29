package main

// `realm-id explain <error_code>` — look an error code up in the SDK's
// partner-facing error reference and say what it means and what to do.
//
// The data is sdk/docs/error-reference.md, vendored by
// scripts/revendor-error-reference.sh at a pinned sdk commit
// (ERROR_REFERENCE_CONTRACT; integrity test in explain_contract_test.go). It
// is read-only and offline: nothing here calls the API.
//
// The tables come in two header shapes — `Code | When | What to do` and
// `Code | Meaning | What to do` — and a cell may hold a `|` inside backticks,
// so rows are split on pipes OUTSIDE backtick spans. A code can appear in more
// than one section; explain returns every row, each with its section.

import (
	_ "embed"
	"fmt"
	"os"
	"sort"
	"strings"
)

//go:embed error-reference.md
var errorReferenceMD []byte

//go:embed ERROR_REFERENCE_CONTRACT
var errorReferenceContract string

// errorReferenceSHA is the sdk commit the embedded reference was vendored at.
func errorReferenceSHA() string {
	for _, line := range strings.Split(errorReferenceContract, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && strings.TrimSpace(k) == "sdk_sha" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// errorRow is one table row. Exactly one of When / Meaning is set, according
// to the table's header shape.
type errorRow struct {
	Code     string `json:"-"`
	Section  string `json:"section"`
	When     string `json:"when,omitempty"`
	Meaning  string `json:"meaning,omitempty"`
	WhatToDo string `json:"what_to_do"`
}

// splitCells splits a markdown table row on pipes that are not inside a
// backtick span, trimming the outer pipes and each cell.
func splitCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	var cells []string
	var cur strings.Builder
	inCode := false
	for _, r := range line {
		switch {
		case r == '`':
			inCode = !inCode
			cur.WriteRune(r)
		case r == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

// codeSpans returns the contents of each backtick span in a code cell.
func codeSpans(cell string) []string {
	var out []string
	parts := strings.Split(cell, "`")
	for i := 1; i < len(parts); i += 2 {
		if s := strings.TrimSpace(parts[i]); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func isSeparatorRow(cells []string) bool {
	for _, c := range cells {
		if c == "" || strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return true
}

// parseErrorReference extracts every code row from the reference, in file order.
func parseErrorReference(md []byte) []errorRow {
	var rows []errorRow
	section, shape := "", "" // shape: "" (no code table open), "when" or "meaning"
	for _, line := range strings.Split(string(md), "\n") {
		if strings.HasPrefix(line, "#") {
			section = strings.TrimSpace(strings.TrimLeft(line, "#"))
			shape = ""
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			if strings.TrimSpace(line) != "" {
				shape = "" // prose ends a table
			}
			continue
		}
		cells := splitCells(line)
		if len(cells) < 3 || isSeparatorRow(cells) {
			continue
		}
		if strings.EqualFold(cells[0], "Code") {
			shape = ""
			switch strings.ToLower(cells[1]) {
			case "when":
				shape = "when"
			case "meaning":
				shape = "meaning"
			}
			continue
		}
		if shape == "" {
			continue
		}
		// A code cell may name several codes (`a` / `b`): one row per code.
		codes := codeSpans(cells[0])
		if len(codes) == 0 {
			codes = []string{strings.ReplaceAll(cells[0], "`", "")}
		}
		for _, c := range codes {
			r := errorRow{Code: c, Section: section, WhatToDo: cells[2]}
			if shape == "when" {
				r.When = cells[1]
			} else {
				r.Meaning = cells[1]
			}
			rows = append(rows, r)
		}
	}
	return rows
}

type explainResult struct {
	helperEnvelope
	Code        string     `json:"code"`
	SourceSHA   string     `json:"source_sha"`
	Matches     []errorRow `json:"matches"`
	Suggestions []string   `json:"suggestions"`
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// suggestCodes returns up to 5 known codes near q: a shared prefix (either
// way, at least 4 chars) or a small edit distance, nearest first.
func suggestCodes(q string, rows []errorRow) []string {
	type cand struct {
		code string
		d    int
	}
	seen := map[string]bool{}
	var cs []cand
	limit := max(2, len(q)/4)
	for _, r := range rows {
		if seen[r.Code] {
			continue
		}
		seen[r.Code] = true
		d := levenshtein(q, r.Code)
		prefix := len(q) >= 4 && (strings.HasPrefix(r.Code, q) || strings.HasPrefix(q, r.Code) && len(r.Code) >= 4)
		if d <= limit || prefix {
			cs = append(cs, cand{r.Code, d})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].d != cs[j].d {
			return cs[i].d < cs[j].d
		}
		return cs[i].code < cs[j].code
	})
	out := []string{}
	for i := 0; i < len(cs) && i < 5; i++ {
		out = append(out, cs[i].code)
	}
	return out
}

func explainCode(code string, rows []errorRow) explainResult {
	res := explainResult{helperEnvelope: newEnvelope("explain"), Code: code, SourceSHA: errorReferenceSHA(),
		Matches: []errorRow{}, Suggestions: []string{}}
	for _, r := range rows {
		if r.Code == code {
			res.Matches = append(res.Matches, r)
		}
	}
	if len(res.Matches) == 0 {
		res.Suggestions = suggestCodes(code, rows)
		msg := fmt.Sprintf("%q is not in the error reference", code)
		if len(res.Suggestions) > 0 {
			msg += "; did you mean: " + strings.Join(res.Suggestions, ", ")
		}
		res.Errors = append(res.Errors, helperError{Code: "unknown_error_code", Message: msg})
		return res
	}
	res.OK = true
	return res
}

func cmdExplain(args []string) int {
	h, err := parseHelperArgs(args, "output")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUsage
	}
	if len(h.pos) != 1 {
		fmt.Fprintln(os.Stderr, "usage: realm-id explain [--output json|text] <error_code>")
		return exitUsage
	}
	if strings.TrimSpace(h.pos[0]) == "messaging" {
		return explainMessaging(wantJSON(h.flags["output"]))
	}
	res := explainCode(strings.TrimSpace(h.pos[0]), append(parseErrorReference(errorReferenceMD), cliLocalErrorRows()...))
	if wantJSON(h.flags["output"]) {
		writeJSON(res)
	} else {
		printExplainText(res)
	}
	if !res.OK {
		return exitErr
	}
	return exitOK
}

func printExplainText(r explainResult) {
	w := os.Stdout
	if !r.OK {
		for _, e := range r.Errors {
			fmt.Fprintln(w, e.Code+": "+e.Message)
		}
		return
	}
	for i, m := range r.Matches {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s  (%s)\n", r.Code, m.Section)
		if m.When != "" {
			fmt.Fprintln(w, "  When:       "+m.When)
		} else {
			fmt.Fprintln(w, "  Meaning:    "+m.Meaning)
		}
		fmt.Fprintln(w, "  What to do: "+m.WhatToDo)
	}
	fmt.Fprintln(w, "\nsource: sdk docs/error-reference.md @ "+r.SourceSHA)
}
