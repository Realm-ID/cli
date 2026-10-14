package main

// Shared plumbing for the hand-written helper commands (verify-token, doctor,
// plan-import, ...). These are NOT part of the generated command tree
// (ADR-062 §1): they are dispatched by name in run() before the tree lookup,
// so they cannot shadow or be shadowed by a generated resource, and
// skipDestructive() never sees them (none of them writes to the API).

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// helperArgs is the result of splitting a helper command's argv.
type helperArgs struct {
	flags map[string]string
	pos   []string
}

// parseHelperArgs splits args into positionals and `--name value` /
// `--name=value` flags. Only names in valueFlags are accepted; anything else
// that starts with "--" is a usage error rather than a silently ignored typo.
func parseHelperArgs(args []string, valueFlags ...string) (*helperArgs, error) {
	known := map[string]bool{}
	for _, f := range valueFlags {
		known[f] = true
	}
	h := &helperArgs{flags: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") || a == "--" {
			h.pos = append(h.pos, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if !known[name] {
			return nil, fmt.Errorf("unknown flag --%s", name)
		}
		if !hasVal {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --%s needs a value", name)
			}
			i++
			val = args[i]
		}
		h.flags[name] = val
	}
	return h, nil
}

// wantJSON applies the CLI's output convention (see renderOutput): an explicit
// --output wins; otherwise JSON unless stdout is a terminal, so an agent
// reading a pipe always gets parseable JSON.
func wantJSON(mode string) bool {
	switch mode {
	case "json":
		return true
	case "table", "text":
		return false
	}
	return !stdoutTTY()
}

func writeJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(os.Stdout, string(b))
}

// helperError is one structured failure: a stable snake_case code an agent can
// branch on, plus a human message. Codes are never embedded in free text.
type helperError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// helperEnvelope is the common top of every helper command's JSON output:
// {"command", "ok", "errors": [...], ...command-specific fields}. Embed it.
type helperEnvelope struct {
	Command string        `json:"command"`
	OK      bool          `json:"ok"`
	Errors  []helperError `json:"errors"`
}

func newEnvelope(command string) helperEnvelope {
	return helperEnvelope{Command: command, Errors: []helperError{}}
}

// exitIncomplete: a helper ran, found no failure, but could not run every
// check (doctor with a SKIP). Distinct from 0 so an agent never reads "didn't
// look" as "all clear". Free in the ADR-062 §1 table (4 conflict, 5 not found, 7 forbidden).
const exitIncomplete = 3
