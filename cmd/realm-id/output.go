package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

// renderOutput writes the response. mode is "json", "table", or "" (auto:
// table on a TTY, json when piped — agents always get parseable JSON).
func renderOutput(raw []byte, mode string) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if mode == "" {
		if stdoutTTY() {
			mode = "table"
		} else {
			mode = "json"
		}
	}
	if mode == "table" {
		if printTable(raw) {
			return nil
		}
		// Not tabular — fall through to pretty JSON.
	}
	return printJSON(raw)
}

func printJSON(raw []byte) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Not JSON — echo as-is.
		_, _ = os.Stdout.Write(raw)
		if !bytes.HasSuffix(raw, []byte("\n")) {
			fmt.Fprintln(os.Stdout)
		}
		return nil
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, string(b))
	return nil
}

// printTable renders a JSON array of flat objects (or an envelope wrapping one)
// as an aligned table. Returns false if the shape isn't tabular.
func printTable(raw []byte) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	rows := asRows(v)
	if rows == nil {
		return false
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stdout, "(no rows)")
		return true
	}
	cols := columns(rows)
	if len(cols) == 0 {
		return false
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(upper(cols), "\t"))
	for _, r := range rows {
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = scalarString(r[c])
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	_ = tw.Flush()
	return true
}

// asRows extracts a list of objects from a bare array or a common envelope
// ({"data":[…]} / {"items":[…]} / {"<one array field>":[…]}).
func asRows(v any) []map[string]any {
	switch t := v.(type) {
	case []any:
		return objs(t)
	case map[string]any:
		for _, key := range []string{"data", "items", "results", "platforms", "tenants", "users"} {
			if arr, ok := t[key].([]any); ok {
				return objs(arr)
			}
		}
		// Single envelope with exactly one array field → use it.
		var only []any
		count := 0
		for _, val := range t {
			if arr, ok := val.([]any); ok {
				only, count = arr, count+1
			}
		}
		if count == 1 {
			return objs(only)
		}
		// A single object → one-row table.
		if isFlat(t) {
			return []map[string]any{t}
		}
	}
	return nil
}

func objs(arr []any) []map[string]any {
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok || !isFlat(m) {
			return nil
		}
		out = append(out, m)
	}
	return out
}

// isFlat reports whether an object's values are all scalars (table-friendly).
func isFlat(m map[string]any) bool {
	for _, v := range m {
		switch v.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}

// columns returns a stable column order: the keys of the first row, then any
// new keys from later rows, appended in sorted order.
func columns(rows []map[string]any) []string {
	seen := map[string]bool{}
	var cols []string
	for k := range rows[0] {
		if !seen[k] {
			seen[k] = true
			cols = append(cols, k)
		}
	}
	sort.Strings(cols)
	var extra []string
	for _, r := range rows[1:] {
		for k := range r {
			if !seen[k] {
				seen[k] = true
				extra = append(extra, k)
			}
		}
	}
	sort.Strings(extra)
	return append(cols, extra...)
}

func upper(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.ToUpper(s)
	}
	return out
}

func scalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func stdoutTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func stdinPiped() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice == 0
}
