package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
)

// resourceTree is the parsed, deduped command tree indexed for dispatch.
type resourceTree struct {
	byGroup map[string]map[string]command // "platforms" | "admin platforms" -> verb -> command
	groups  []string                      // sorted group keys, for help
	dropped []command
}

func loadTree() (*resourceTree, error) {
	cmds, dropped, err := buildCommands()
	if err != nil {
		return nil, err
	}
	t := &resourceTree{byGroup: map[string]map[string]command{}, dropped: dropped}
	for _, c := range cmds {
		key := strings.Join(c.Group, " ")
		if t.byGroup[key] == nil {
			t.byGroup[key] = map[string]command{}
			t.groups = append(t.groups, key)
		}
		t.byGroup[key][c.Verb] = c
	}
	sort.Strings(t.groups)
	return t, nil
}

// isResource reports whether args[0] begins a generated resource command, so
// main can route to it (vs. an unknown-command error).
func (t *resourceTree) isResource(token string) bool {
	for _, g := range t.groups {
		if g == token || strings.HasPrefix(g, token+" ") {
			return true
		}
	}
	return false
}

// resolveGroup consumes leading group tokens (depth 1 or 2) from args and
// returns the group key plus the index of the verb token.
func (t *resourceTree) resolveGroup(args []string) (key string, verbIdx int, ok bool) {
	if len(args) >= 2 {
		two := args[0] + " " + args[1]
		if _, found := t.byGroup[two]; found {
			return two, 2, true
		}
	}
	if _, found := t.byGroup[args[0]]; found {
		return args[0], 1, true
	}
	return "", 0, false
}

func cmdResource(args []string) int {
	t, err := loadTree()
	if err != nil {
		return fail(fmt.Errorf("loading command tree: %w", err))
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}

	key, verbIdx, ok := t.resolveGroup(args)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown resource %q\n\n", args[0])
		t.printGroups(os.Stderr)
		return exitUsage
	}
	verbs := t.byGroup[key]
	if verbIdx >= len(args) || args[verbIdx] == "--help" || args[verbIdx] == "-h" {
		t.printVerbs(os.Stdout, key)
		return exitOK
	}
	verb := args[verbIdx]
	cmd, found := verbs[verb]
	if !found {
		fmt.Fprintf(os.Stderr, "unknown verb %q for %q\n\n", verb, key)
		t.printVerbs(os.Stderr, key)
		return exitUsage
	}
	return runCommand(cfg, cmd, args[verbIdx+1:])
}

// parsedFlags holds the raw --k v / --k=v flags for a command invocation.
type parsedFlags struct {
	vals   map[string]string
	fields []string // repeatable --field k=v
	help   bool
}

// boolFlags never take a value.
var boolFlags = map[string]bool{"help": true, "h": true}

func parseFlags(args []string) (*parsedFlags, error) {
	pf := &parsedFlags{vals: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return nil, fmt.Errorf("unexpected argument %q (use --flags)", a)
		}
		name := strings.TrimLeft(a, "-")
		var val string
		hasInline := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			val, name, hasInline = name[eq+1:], name[:eq], true
		}
		if name == "help" || name == "h" {
			pf.help = true
			continue
		}
		if !hasInline {
			if boolFlags[name] {
				val = "true"
			} else {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("flag --%s needs a value", name)
				}
				i++
				val = args[i]
			}
		}
		if name == "field" || name == "f" {
			pf.fields = append(pf.fields, val)
			continue
		}
		pf.vals[name] = val
	}
	return pf, nil
}

func runCommand(cfg *Config, cmd command, args []string) int {
	pf, err := parseFlags(args)
	if err != nil {
		return failCode(err, exitUsage)
	}
	if pf.help {
		printCommandHelp(os.Stdout, cmd)
		return exitOK
	}

	// Resolve path params.
	path := cmd.Path
	for _, p := range cmd.Params {
		val, verr := resolveParam(cfg, p, pf)
		if verr != nil {
			return failCode(verr, exitUsage)
		}
		path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(val))
	}

	// Query string from declared query params.
	q := url.Values{}
	for _, qp := range cmd.Query {
		if v, ok := pf.vals[qp.Name]; ok {
			q.Set(qp.Name, v)
		}
	}
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}

	// Body.
	body, berr := buildBody(cmd, pf)
	if berr != nil {
		return failCode(berr, exitUsage)
	}

	base, bearer := resolveCredential(cfg)
	if bearer == "" {
		return failCode(fmt.Errorf("no credential: set REALM_ID_API_KEY or run `realm-id auth login`"), exitForbidden)
	}

	req, err := newRequest(cmd.Method, base+path, bearer, body, pf.vals["as-user"])
	if err != nil {
		return fail(err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))

	if err := renderOutput(raw, pf.vals["output"]); err != nil {
		return fail(err)
	}
	return exitForStatus(resp.StatusCode)
}

// resolveParam fills a path param from active context or an explicit flag.
func resolveParam(cfg *Config, p pathParam, pf *parsedFlags) (string, error) {
	switch p.Role {
	case "platform":
		if v := pf.vals["platform"]; v != "" {
			return v, nil
		}
		if v := pf.vals[p.Name]; v != "" {
			return v, nil
		}
		if cfg.Platform != "" {
			return cfg.Platform, nil
		}
		return "", fmt.Errorf("no active platform: `realm-id config set platform <id>` or pass --platform")
	case "tenant":
		if v := pf.vals["tenant"]; v != "" {
			return v, nil
		}
		if v := pf.vals[p.Name]; v != "" {
			return v, nil
		}
		// Fall back to the active tenant chosen at the CLI level (ADR-062 §2):
		// `realm-id config set tenant <id>` or auto-picked at login.
		if cfg.Tenant != "" {
			return cfg.Tenant, nil
		}
		return "", fmt.Errorf("no active tenant: `realm-id config set tenant <id>` or pass --tenant")
	default:
		if v := pf.vals[p.Name]; v != "" {
			return v, nil
		}
		return "", fmt.Errorf("missing required --%s", p.Name)
	}
}

// buildBody assembles the request body from --json, repeated --field k=v, or
// piped stdin. Returns nil when the operation takes no body.
func buildBody(cmd command, pf *parsedFlags) (any, error) {
	if raw, ok := pf.vals["json"]; ok {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, fmt.Errorf("invalid --json: %w", err)
		}
		return v, nil
	}
	if len(pf.fields) > 0 {
		obj := map[string]any{}
		for _, f := range pf.fields {
			eq := strings.IndexByte(f, '=')
			if eq < 0 {
				return nil, fmt.Errorf("--field %q must be key=value (or key:=rawjson)", f)
			}
			k, val := f[:eq], f[eq+1:]
			if strings.HasSuffix(k, ":") { // key:=rawjson — typed value
				k = strings.TrimSuffix(k, ":")
				var jv any
				if err := json.Unmarshal([]byte(val), &jv); err != nil {
					return nil, fmt.Errorf("--field %s:= has invalid JSON: %w", k, err)
				}
				obj[k] = jv
				continue
			}
			obj[k] = inferScalar(val)
		}
		return obj, nil
	}
	if cmd.HasBody && stdinPiped() {
		raw, _ := io.ReadAll(io.LimitReader(os.Stdin, 16<<20))
		if len(strings.TrimSpace(string(raw))) > 0 {
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, fmt.Errorf("invalid JSON on stdin: %w", err)
			}
			return v, nil
		}
	}
	return nil, nil
}

// inferScalar gives --field bare values natural JSON types (number/bool/null).
func inferScalar(s string) any {
	switch s {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return n
	}
	return s
}

// resolveCredential picks the base URL + bearer. A service key (REALM_ID_API_KEY)
// talks issuer-direct (ADR-062 §4 Service mode); otherwise the device-flow
// session bearer is used. The typed admin surface is the issuer's contract, so
// both modes target the issuer.
func resolveCredential(cfg *Config) (base, bearer string) {
	base = cfg.issuerURL()
	if k := envOr("REALM_ID_API_KEY", ""); k != "" {
		return base, k
	}
	return base, cfg.SessionToken
}

func printCommandHelp(w io.Writer, cmd command) {
	fmt.Fprintf(w, "realm-id %s %s — %s\n\n", strings.Join(cmd.Group, " "), cmd.Verb, cmd.Summary)
	fmt.Fprintf(w, "  %s %s\n\n", cmd.Method, cmd.Path)
	for _, p := range cmd.Params {
		switch p.Role {
		case "platform":
			fmt.Fprintf(w, "  --platform <id>   platform (defaults to active config)\n")
		case "tenant":
			fmt.Fprintf(w, "  --tenant <id>     tenant (required)\n")
		default:
			fmt.Fprintf(w, "  --%-14s (required)\n", p.Name+" <val>")
		}
	}
	for _, q := range cmd.Query {
		fmt.Fprintf(w, "  --%-14s (filter)\n", q.Name+" <val>")
	}
	if cmd.HasBody {
		fmt.Fprintf(w, "  --json '<obj>' | --field k=v … | (JSON on stdin)\n")
	}
	fmt.Fprintf(w, "  --output json|table\n")
}

func (t *resourceTree) printGroups(w io.Writer) {
	fmt.Fprintln(w, "Resources:")
	seen := map[string]bool{}
	for _, g := range t.groups {
		top := strings.Fields(g)[0]
		if seen[top] {
			continue
		}
		seen[top] = true
		fmt.Fprintf(w, "  %s\n", top)
	}
	fmt.Fprintln(w, "\nRun `realm-id <resource>` to list its verbs, or `realm-id schema` for the full API.")
}

func (t *resourceTree) printVerbs(w io.Writer, key string) {
	verbs := make([]string, 0, len(t.byGroup[key]))
	for v := range t.byGroup[key] {
		verbs = append(verbs, v)
	}
	sort.Strings(verbs)
	fmt.Fprintf(w, "realm-id %s — verbs:\n", key)
	for _, v := range verbs {
		fmt.Fprintf(w, "  %-12s %s\n", v, t.byGroup[key][v].Summary)
	}
}

func newRequest(method, fullURL, bearer string, body any, asUser string) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, fullURL, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if asUser != "" {
		// ADR-056: forward an on-behalf-of user token to the issuer/BFF.
		req.Header.Set("X-User-Token", asUser)
	}
	return req, nil
}
