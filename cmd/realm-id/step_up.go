package main

// Credentials, session classes and step-up for split signing v3 (plan v3 §3.9 / §7.2; v2 §5.1 and
// W1.10; R6, R24, R40, R41, R63, R72, R73).
//
// There is no service mode any more: the CLI never calls the issuer. A person signs in by device
// flow; a `uk_live_` user key in REALM_ID_API_KEY is exchanged at the BFF for a KEY session; a CI
// runner's OIDC token is exchanged at the BFF for a WORKLOAD session (`auth workload`). Every
// typed call then rides the BFF `/api/*` passthrough under that session's bearer.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// platformTokenCache holds the key-session bearer for the lifetime of the PROCESS: one exchange
// per run, never persisted (the key itself is the renewable credential).
var platformTokenCache string

// usageCredError marks a credential problem that is the caller's usage (exit 2), not an auth failure.
type usageCredError struct{ msg string }

func (e usageCredError) Error() string { return e.msg }

func credentialExit(err error) int {
	var u usageCredError
	if errors.As(err, &u) {
		return exitUsage
	}
	return exitUnauthenticated
}

// sessionInfo is the BFF's answer to /auth/key-session and /auth/workload-session (the BFF may wrap
// it in GoFr's {"data":…} envelope).
type sessionInfo struct {
	SessionToken string `json:"session_token"`
	RealmID      string `json:"realm_id"`
	TenantID     string `json:"tenant_id"`
	SessionClass string `json:"session_class"`
	Role         string `json:"role"`
}

// resolveCredential picks the base URL + bearer for a typed command: always the BFF passthrough.
func resolveCredential(cfg *Config) (base, bearer string, err error) {
	if k := envOr("REALM_ID_API_KEY", ""); k != "" {
		tok, xerr := exchangeUserKey(cfg, k)
		if xerr != nil {
			return "", "", xerr
		}
		return cfg.bffURL() + "/api", tok, nil
	}
	return cfg.bffURL() + "/api", cfg.SessionToken, nil
}

// exchangeUserKey trades a user API key for a key session at the BFF. The key travels in the BODY,
// never as a bearer. A platform key (rk_live_) is refused offline: it has no session path (R24).
func exchangeUserKey(cfg *Config, apiKey string) (string, error) {
	if strings.HasPrefix(apiKey, "rk_live_") {
		return "", usageCredError{"REALM_ID_API_KEY holds a platform key (rk_live_). The CLI no longer signs in with one: " +
			"mint a user key with `realm-id user-api-keys create` and put that (uk_live_…) in REALM_ID_API_KEY."}
	}
	if platformTokenCache != "" {
		return platformTokenCache, nil
	}
	body := map[string]string{"api_key": apiKey}
	if cfg.Tenant != "" {
		body["tenant_id"] = cfg.Tenant
	}
	var out sessionInfo
	status, raw, err := jsonRequest(http.MethodPost, cfg.bffURL()+"/auth/key-session", "", body, &out)
	if err != nil {
		return "", fmt.Errorf("exchanging REALM_ID_API_KEY for a key session: %w", err)
	}
	if status/100 != 2 {
		return "", fmt.Errorf("exchanging REALM_ID_API_KEY for a key session: %s: %s",
			http.StatusText(status), strings.TrimSpace(string(raw)))
	}
	if out.SessionToken == "" {
		return "", errors.New("the BFF returned no session_token for the key session")
	}
	platformTokenCache = out.SessionToken
	return platformTokenCache, nil
}

// credentialClass names the session behind this run: "key", "workload" or "human".
func credentialClass(cfg *Config) (class, role string) {
	switch {
	case envOr("REALM_ID_API_KEY", "") != "":
		return "key", ""
	case cfg.SessionClass != "":
		return cfg.SessionClass, cfg.SessionRole
	}
	return "human", ""
}

// needsHuman refuses a person-only verb in a key or workload session BEFORE anything is sent
// (R40/R73). mgmtOK admits R72's one exception: a workload session whose actor role is
// platform_mgmt_api. Returns 0 to proceed, else the exit code (8) with the JSON envelope on stderr.
func needsHuman(cfg *Config, verb string, mgmtOK bool) int {
	class, role := credentialClass(cfg)
	if class == "human" || (mgmtOK && class == "workload" && role == "platform_mgmt_api") {
		return 0
	}
	env := map[string]any{"error": map[string]string{
		"code":    "needs_human",
		"message": fmt.Sprintf("`%s` needs a person: a %s session cannot run it", verb, class),
		"hint":    "Run `realm-id auth login` as the realm owner and repeat the command (see `realm-id explain needs_human`).",
	}}
	b, _ := json.Marshal(env)
	fmt.Fprintln(os.Stderr, string(b))
	return exitNeedsHuman
}

// authWorkload exchanges the CI runner's OIDC token (GitHub's ACTIONS_ID_TOKEN_REQUEST_*) for a
// workload session at the BFF and stores it as the current session.
func authWorkload(cfg *Config, args []string) int {
	h, err := parseHelperArgs(args, "audience")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUsage
	}
	reqURL, reqTok := envOr("ACTIONS_ID_TOKEN_REQUEST_URL", ""), envOr("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
	if reqURL == "" || reqTok == "" {
		return failCode(errors.New("no OIDC token source: ACTIONS_ID_TOKEN_REQUEST_URL / _TOKEN are not set "+
			"(grant the job `permissions: id-token: write`)"), exitUsage)
	}
	if aud := h.flags["audience"]; aud != "" {
		u, perr := url.Parse(reqURL)
		if perr != nil {
			return failCode(fmt.Errorf("ACTIONS_ID_TOKEN_REQUEST_URL: %w", perr), exitUsage)
		}
		q := u.Query()
		q.Set("audience", aud)
		u.RawQuery = q.Encode()
		reqURL = u.String()
	}
	var oidc struct {
		Value string `json:"value"`
	}
	st, _, err := jsonRequest(http.MethodGet, reqURL, reqTok, nil, &oidc)
	if err != nil || st/100 != 2 || oidc.Value == "" {
		return failCode(fmt.Errorf("fetching the runner's OIDC token failed (status %d): %v", st, err), exitUnauthenticated)
	}
	var out sessionInfo
	st, raw, err := jsonRequest(http.MethodPost, cfg.bffURL()+"/auth/workload-session", "",
		map[string]string{"subject_token": oidc.Value}, &out)
	if err != nil {
		return fail(err)
	}
	if st/100 != 2 || out.SessionToken == "" {
		fmt.Fprintln(os.Stderr, strings.TrimSpace(string(raw)))
		if st/100 == 2 {
			return failCode(errors.New("the BFF returned no session_token"), exitErr)
		}
		return exitForStatus(st)
	}
	cfg.SessionToken, cfg.SessionClass, cfg.SessionRole = out.SessionToken, "workload", out.Role
	if out.TenantID != "" {
		cfg.Tenant = out.TenantID
	}
	if err := saveConfig(cfg); err != nil {
		return fail(err)
	}
	fmt.Fprintf(os.Stderr, "Workload session saved (role %q).\n", out.Role)
	return exitOK
}

// ---- BFF calls with step-up ----

// bffCall sends one JSON request through the BFF `/api/*` passthrough under the run's credential.
func bffCall(cfg *Config, method, path string, body any) (int, []byte, error) {
	base, bearer, err := resolveCredential(cfg)
	if err != nil {
		return 0, nil, err
	}
	if bearer == "" {
		return 0, nil, usageNoSession{}
	}
	return jsonRequest(method, base+path, bearer, body, nil)
}

// bffCallInto is bffCall on the BFF root (not /api) decoding into out; used for native BFF routes.
func bffCallInto(cfg *Config, method, path string, out any) (int, []byte, error) {
	_, bearer, err := resolveCredential(cfg)
	if err != nil {
		return 0, nil, err
	}
	return jsonRequest(method, cfg.bffURL()+path, bearer, nil, out)
}

type usageNoSession struct{}

func (usageNoSession) Error() string { return "not signed in (run: realm-id auth login)" }

// callResult folds a transport error / missing session into an exit code; ok is false when the
// caller must return code.
func callExit(err error) int {
	var ns usageNoSession
	if errors.As(err, &ns) {
		return failCode(err, exitUnauthenticated)
	}
	return failCode(err, credentialExit(err))
}

// challengeToken pulls mfa_challenge_token from a 412 body, top level or under "error".
func challengeToken(raw []byte) string {
	var b struct {
		Tok string `json:"mfa_challenge_token"`
		Err struct {
			Tok string `json:"mfa_challenge_token"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &b)
	if b.Tok != "" {
		return b.Tok
	}
	return b.Err.Tok
}

// stepUpCall sends a mutation and, on a 412 step-up, verifies a TOTP code (from --totp, else a
// prompt on a terminal) and retries the SAME request once (R40/R41). abort >= 0 means stop with
// that exit code (already reported).
func stepUpCall(cfg *Config, method, path string, body any, totp string) (status int, raw []byte, abort int) {
	st, raw, err := bffCall(cfg, method, path, body)
	if err != nil {
		return 0, nil, callExit(err)
	}
	if st != http.StatusPreconditionFailed {
		return st, raw, -1
	}
	tok := challengeToken(raw)
	if tok == "" {
		return st, raw, -1
	}
	if totp == "" {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Fprintln(os.Stderr, "error: this operation needs a TOTP code (step-up); pass --totp <code>")
			return 0, nil, exitIncomplete
		}
		fmt.Fprint(os.Stderr, "TOTP code: ")
		b, rerr := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if rerr != nil {
			return 0, nil, failCode(fmt.Errorf("reading the TOTP code: %w", rerr), exitErr)
		}
		totp = strings.TrimSpace(string(b))
	}
	_, bearer, _ := resolveCredential(cfg)
	vst, vraw, verr := jsonRequest(http.MethodPost, cfg.bffURL()+"/auth/mfa/verify", bearer,
		map[string]string{"mfa_challenge_token": tok, "code": totp, "method": "totp"}, nil)
	if verr != nil {
		return 0, nil, fail(verr)
	}
	if vst/100 != 2 {
		fmt.Fprintln(os.Stderr, strings.TrimSpace(string(vraw)))
		return 0, nil, exitForStatus(vst)
	}
	st, raw, err = bffCall(cfg, method, path, body)
	if err != nil {
		return 0, nil, callExit(err)
	}
	return st, raw, -1
}

// printBody writes an API body to stdout (stdout is the machine channel) and returns its exit.
func printBody(status int, raw []byte) int {
	if len(bytes.TrimSpace(raw)) > 0 {
		_, _ = os.Stdout.Write(bytes.TrimRight(raw, "\n"))
		fmt.Fprintln(os.Stdout)
	}
	return exitForStatus(status)
}

// ---- flag parsing for the hand-written verbs ----

type verbFlags struct {
	vals  map[string]string
	bools map[string]bool
}

// parseVerbFlags accepts only the named value flags and bool flags; anything else is a usage error.
func parseVerbFlags(args []string, valueFlags, boolFlags []string) (*verbFlags, error) {
	known, isBool := map[string]bool{}, map[string]bool{}
	for _, f := range valueFlags {
		known[f] = true
	}
	for _, f := range boolFlags {
		isBool[f] = true
	}
	v := &verbFlags{vals: map[string]string{}, bools: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			return nil, fmt.Errorf("unexpected argument %q (use --flags)", a)
		}
		name, val, hasVal := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		switch {
		case isBool[name]:
			v.bools[name] = !hasVal || val == "true"
		case known[name]:
			if !hasVal {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("flag --%s needs a value", name)
				}
				i++
				val = args[i]
			}
			v.vals[name] = val
		default:
			return nil, fmt.Errorf("unknown flag --%s", name)
		}
	}
	return v, nil
}

// ---- user-api-keys create, platform-keys create ----

// cmdUserAPIKeysCreate mints a user API key (hand-written: the mint is filtered out of the
// generated tree, ADR-062). A person's session only (R40); 412 step-up takes --totp.
func cmdUserAPIKeysCreate(args []string) int {
	f, err := parseVerbFlags(args, []string{"tenant", "user", "label", "permissions", "totp", "ttl-seconds"}, []string{"uncapped"})
	if err != nil {
		return failCode(err, exitUsage)
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	if code := needsHuman(cfg, "user-api-keys create", false); code != 0 {
		return code
	}
	tenant := f.vals["tenant"]
	if tenant == "" {
		tenant = cfg.Tenant
	}
	if tenant == "" || f.vals["label"] == "" {
		return failCode(errors.New("usage: realm-id user-api-keys create --tenant <id> --label <text> (--permissions a:b,c:d | --uncapped) [--user <id>] [--totp <code>]"), exitUsage)
	}
	body := map[string]any{"label": f.vals["label"]}
	switch {
	case f.bools["uncapped"] && f.vals["permissions"] != "":
		return failCode(errors.New("--uncapped and --permissions contradict each other"), exitUsage)
	case f.bools["uncapped"]:
		body["uncapped"] = true
	case f.vals["permissions"] != "":
		body["uncapped"] = false
		body["permissions_cap"] = strings.Split(f.vals["permissions"], ",")
	default:
		return failCode(errors.New("state the key's authority: --permissions a:b,c:d or --uncapped (ADR-100)"), exitUsage)
	}
	if t := f.vals["ttl-seconds"]; t != "" {
		n, perr := strconv.Atoi(t)
		if perr != nil {
			return failCode(fmt.Errorf("--ttl-seconds: %w", perr), exitUsage)
		}
		body["ttl_seconds"] = n
	}
	user := f.vals["user"]
	if user == "" { // the key is self-service: {uid} must be the caller
		var me struct {
			UserID string `json:"user_id"`
		}
		if mst, _, merr := bffCallInto(cfg, http.MethodGet, "/me", &me); merr == nil && mst/100 == 2 {
			user = me.UserID
		}
		if user == "" {
			user = "me"
		}
	}
	path := "/tenants/" + url.PathEscape(tenant) + "/users/" + url.PathEscape(user) + "/user-api-keys"
	st, raw, abort := stepUpCall(cfg, http.MethodPost, path, body, f.vals["totp"])
	if abort >= 0 {
		return abort
	}
	return printBody(st, raw)
}

// cmdPlatformKeys mints a platform key (R72: a person, or the platform_mgmt_api workload actor).
func cmdPlatformKeys(args []string) int {
	if len(args) == 0 || args[0] != "create" {
		fmt.Fprintln(os.Stderr, "usage: realm-id platform-keys create --realm <id> --label <text> [--totp <code>]")
		return exitUsage
	}
	f, err := parseVerbFlags(args[1:], []string{"realm", "label", "totp"}, nil)
	if err != nil {
		return failCode(err, exitUsage)
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	if code := needsHuman(cfg, "platform-keys create", true); code != 0 {
		return code
	}
	realm := f.vals["realm"]
	if realm == "" {
		realm = cfg.Platform
	}
	if realm == "" || f.vals["label"] == "" {
		return failCode(errors.New("usage: realm-id platform-keys create --realm <id> --label <text>"), exitUsage)
	}
	st, raw, abort := stepUpCall(cfg, http.MethodPost, "/platforms/"+url.PathEscape(realm)+"/api-keys",
		map[string]any{"label": f.vals["label"]}, f.vals["totp"])
	if abort >= 0 {
		return abort
	}
	return printBody(st, raw)
}
