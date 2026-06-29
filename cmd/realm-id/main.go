// Command realm-id is the agent-first CLI for the RealmID identity platform
// (ADR-062). It drives onboarding and platform configuration for humans and
// AI agents alike: gcloud-shaped noun/verb commands, JSON output, and an
// `auth login` device flow so a terminal/agent can authenticate via a link.
//
// This is the v1 core (auth + config + a generic authenticated request). The
// full typed command tree is generated from the issuer OpenAPI spec in a
// follow-up (ADR-062 §1).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

// Exit codes (ADR-062 §1): agents branch on these.
const (
	exitOK        = 0
	exitErr       = 1
	exitUsage     = 2
	exitConflict  = 4
	exitNotFound  = 5
	exitForbidden = 7
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUsage
	}
	switch args[0] {
	case "auth":
		return cmdAuth(args[1:])
	case "config":
		return cmdConfig(args[1:])
	case "api":
		return cmdAPI(args[1:])
	case "schema":
		return cmdSchema(args[1:])
	case "version", "--version", "-v":
		fmt.Fprintln(os.Stdout, "realm-id", version)
		return exitOK
	case "help", "--help", "-h":
		usage()
		return exitOK
	default:
		// Generated typed command tree (ADR-062 §1): `realm-id <resource> <verb>`.
		if t, err := loadTree(); err == nil && t.isResource(args[0]) {
			return cmdResource(args)
		}
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage()
		return exitUsage
	}
}

// cmdSchema dumps the embedded OpenAPI contract so an agent can self-orient
// (ADR-062 §1, progressive disclosure).
func cmdSchema(_ []string) int {
	_, _ = os.Stdout.Write(openapiYAML)
	return exitOK
}

func usage() {
	fmt.Fprint(os.Stderr, `realm-id — CLI for the RealmID identity platform

Usage:
  realm-id auth login              Authenticate via a browser link (device flow)
  realm-id auth whoami             Show the current session identity
  realm-id auth logout             Revoke the session and clear local credentials
  realm-id config set <key> <val>  Set platform | tenant | bff_url | issuer_url
  realm-id config get <key>        Print a config value
  realm-id config list             Show the active configuration
  realm-id <resource> <verb>       Typed API command (e.g. realm-id platforms list)
  realm-id <resource>              List a resource's verbs
  realm-id schema                  Dump the OpenAPI contract (agent self-orientation)
  realm-id api <method> <path>     Raw authenticated request through the BFF (JSON)
  realm-id version                 Print the CLI version

Resources: platforms, tenants, users, invitations, api-keys, roles,
  federation-bindings, origins, domains, identity-providers, audit-events,
  contact-verifications, contact-drift-reviews, mfa, admin

Output: --output json|table (json when piped, table on a TTY)
Scope:  --platform <id> (or active config) · --tenant <id>
Body:   --json '<obj>' · --field k=v (repeatable, key:=rawjson for typed) · stdin

Env: REALM_ID_BFF, REALM_ID_ISSUER, REALM_ID_API_KEY, REALM_ID_CONFIG
`)
}

// ---- auth ----

func cmdAuth(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: realm-id auth <login|whoami|logout>")
		return exitUsage
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	switch args[0] {
	case "login":
		return authLogin(cfg, resolveDeviceName(args[1:]))
	case "whoami":
		return authWhoami(cfg)
	case "logout":
		return authLogout(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown auth subcommand %q\n", args[0])
		return exitUsage
	}
}

type deviceCodeResp struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// tenantInfo mirrors one BFF membership row in the device-token poll response.
type tenantInfo struct {
	ID          string `json:"id"`
	Role        string `json:"role,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// deviceTokenResp is the BFF device-token poll response. tenant_id is set when
// the session was pinned (single-membership user); for a multi-membership user
// it is "" and the CLI selects a tenant from tenants[] (ADR-062 §2).
type deviceTokenResp struct {
	SessionToken string       `json:"session_token"`
	RealmID      string       `json:"realm_id"`
	TenantID     string       `json:"tenant_id"`
	Tenants      []tenantInfo `json:"tenants"`
}

// resolveDeviceName picks the label sent with a device login, in order:
//   --device-name <value>, then $REALM_ID_DEVICE_NAME, then the OS hostname.
// Falls back to "realm-id cli" when the hostname is unavailable. The label
// helps a user tell this session apart in their session list (ADR-062).
func resolveDeviceName(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--device-name" && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(args[i], "--device-name="); ok {
			return v
		}
	}
	if v := envOr("REALM_ID_DEVICE_NAME", ""); v != "" {
		return v
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "realm-id cli"
}

// openBrowser best-effort opens url in the user's default browser and reports
// whether the launcher started. It deliberately never blocks or fails the
// login: headless servers, SSH sessions and CI have no browser, and the
// printed URL is always the fallback. Set REALM_ID_NO_BROWSER=1 to suppress
// the launch entirely (the documented escape hatch for headless/agent runs).
func openBrowser(url string) bool {
	if envOr("REALM_ID_NO_BROWSER", "") != "" {
		return false
	}
	// Only launch when attached to an interactive terminal. Headless/agent runs
	// (ADR-062's non-interactive callers), CI, and piped output get the printed
	// link instead of a stray browser process.
	if fi, err := os.Stderr.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default: // linux, *bsd
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start() == nil
}

// claimActiveLogin records deviceCode as the active login. A subsequent
// `auth login` overwrites the marker, which any older poller reads to learn it
// was superseded. Best-effort: a write failure just means supersession won't be
// detected, never that the login fails.
func claimActiveLogin(deviceCode string) {
	p, err := activeLoginPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(p, []byte(deviceCode), 0o600)
}

// activeLoginIs reports whether deviceCode is still the active login. A missing
// or unreadable marker is treated as "still active" — we never abort a login
// just because the marker can't be read.
func activeLoginIs(deviceCode string) bool {
	p, err := activeLoginPath()
	if err != nil {
		return true
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(b)) == deviceCode
}

// clearActiveLogin removes the marker iff it still names deviceCode, so a
// finished login doesn't leave a stale marker that would falsely supersede the
// next one. Best-effort.
func clearActiveLogin(deviceCode string) {
	if !activeLoginIs(deviceCode) {
		return
	}
	if p, err := activeLoginPath(); err == nil {
		_ = os.Remove(p)
	}
}

// deviceCodeMaxTTL bounds how long the singleton login lock can be held before
// it is considered stale and reclaimable. It is set a little over the server's
// device-code TTL (~600s) so a crashed/abandoned run's lock self-clears shortly
// after its code would have expired anyway.
const deviceCodeMaxTTL = 11 * time.Minute

// errLoginInProgress is returned by acquireLoginLock when another device login
// is already running on this machine. It is the ONLY acquire failure the caller
// treats as fatal — any other error (e.g. unresolved config dir) is non-fatal
// and the login proceeds lockless, mirroring the best-effort active-login marker.
var errLoginInProgress = errors.New(
	"a device login is already in progress on this machine; cancel it or wait for it to finish before starting another")

func loginLockPath() (string, error) {
	p, err := configPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "login.lock"), nil
}

// acquireLoginLock takes the per-machine device-login singleton lock and returns
// a release func. If a login is already in progress (lock held and not past its
// device-code deadline) it returns errLoginInProgress so the caller refuses
// cleanly — this is the hard mutual exclusion that makes the concurrent-run /
// wrong-tab footgun impossible (ADR-062 §2), not just discouraged. A lock left
// by a crashed or expired run (deadline in the past, or unreadable) is reclaimed.
func acquireLoginLock(ttl time.Duration) (func(), error) {
	p, err := loginLockPath()
	if err != nil {
		return nil, err // non-fatal: caller proceeds lockless
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err // non-fatal
	}
	content := fmt.Sprintf("%d\n%d", os.Getpid(), time.Now().Add(ttl).Unix())
	// Fast path: atomic exclusive create wins the lock outright.
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		_, _ = f.WriteString(content)
		_ = f.Close()
		return func() { releaseLoginLock(p) }, nil
	}
	// Lock file exists. Reclaim it only if the in-flight login's deadline has
	// passed; otherwise a live login owns it → refuse.
	if !loginLockStale(p) {
		return nil, errLoginInProgress
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		return nil, err // non-fatal
	}
	return func() { releaseLoginLock(p) }, nil
}

// loginLockStale reports whether the lock at p is reclaimable: unreadable,
// malformed, or past its recorded deadline. Unreadable/malformed is treated as
// stale so a corrupt marker can never wedge logins forever.
func loginLockStale(p string) bool {
	b, err := os.ReadFile(p)
	if err != nil {
		return true
	}
	parts := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)
	if len(parts) != 2 {
		return true
	}
	deadline, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		return true
	}
	return time.Now().Unix() >= deadline
}

// releaseLoginLock removes the lock iff it still names our pid, so a run that
// went stale and was reclaimed by a newer login never deletes the newer lock.
func releaseLoginLock(p string) {
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	parts := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)
	if len(parts) >= 1 && strings.TrimSpace(parts[0]) == strconv.Itoa(os.Getpid()) {
		_ = os.Remove(p)
	}
}

func authLogin(cfg *Config, deviceName string) int {
	// Hard singleton (ADR-062 §2): refuse a second concurrent device login on
	// this machine. With at most one login live at a time, only one approvable
	// user_code can exist from the CLI's side, so the wrong-tab footgun can't
	// happen. A non-fatal lock error (e.g. unresolved config dir) falls through
	// to the best-effort active-login marker rather than blocking a real login.
	if release, lockErr := acquireLoginLock(deviceCodeMaxTTL); errors.Is(lockErr, errLoginInProgress) {
		return fail(lockErr)
	} else if release != nil {
		defer release()
	}

	bff := cfg.bffURL()
	var dc deviceCodeResp
	// Send a device label so this session is identifiable in the user's
	// session list (ADR-062). Body is optional server-side.
	body := map[string]string{"device_name": deviceName}
	status, _, err := jsonRequest(http.MethodPost, bff+"/auth/device/code", "", body, &dc)
	if err != nil || status >= 400 {
		return fail(fmt.Errorf("starting device login failed (status %d): %v", status, err))
	}
	debugDev := envOr("REALM_ID_DEBUG", "") != ""
	if debugDev {
		fmt.Fprintf(os.Stderr, "[debug] BFF=%s\n[debug] device/code status=%d user_code=%s device_code=%s expires_in=%d interval=%d\n",
			bff, status, dc.UserCode, dc.DeviceCode, dc.ExpiresIn, dc.Interval)
	}
	// The URL embeds THIS run's user_code; the page reads it from the query
	// string so the user never has to type or match a code. Approving any other
	// code (a stale browser tab, or a code typed in by hand) authorizes a
	// different device record that nobody is polling — the #1 device-flow
	// support footgun (ADR-062 §2). Best-effort auto-open lands the user on the
	// exact link; the printed URL stays as the fallback.
	fmt.Fprintf(os.Stderr, "To authorize this CLI, open:\n\n    %s\n\n", dc.VerificationURIComplete)
	if openBrowser(dc.VerificationURIComplete) {
		fmt.Fprintln(os.Stderr, "(opened in your browser — approve the request shown there)")
	} else {
		fmt.Fprintf(os.Stderr, "(if it doesn't open, paste the link above — it already contains code %s)\n", dc.UserCode)
	}
	fmt.Fprintln(os.Stderr, "Waiting for approval...")

	// Become the active login. Any older poller still running sees this and
	// stops on its next tick, so only the newest code is being waited on.
	claimActiveLogin(dc.DeviceCode)

	interval := dc.Interval
	if interval <= 0 {
		interval = 5
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Duration(interval) * time.Second)
		// A newer `auth login` superseded us — stop rather than poll a code the
		// user has abandoned (which would otherwise sit pending its whole TTL).
		if !activeLoginIs(dc.DeviceCode) {
			fmt.Fprintln(os.Stderr, "Superseded by a newer `realm-id auth login`; stopping this one.")
			return exitErr
		}
		var tok deviceTokenResp
		st, raw, _ := jsonRequest(http.MethodPost, bff+"/auth/device/token",
			"", map[string]string{"device_code": dc.DeviceCode}, &tok)
		if debugDev {
			fmt.Fprintf(os.Stderr, "[debug] poll device_code=%s status=%d code=%q\n", dc.DeviceCode, st, errorCode(raw))
		}
		// Accept any 2xx that carries a session token. The BFF runs on GoFr,
		// which returns 201 Created (not 200) for a POST handler — so the
		// approved /auth/device/token response arrives as 201. Hardcoding 200
		// here silently discarded the delivered token and kept polling until the
		// (now single-use-consumed) record expired → bogus "expired before
		// approval" on every successful approval. Match the whole 2xx class.
		if st/100 == 2 && tok.SessionToken != "" {
			cfg.SessionToken = tok.SessionToken
			cfg.Tenant = tok.TenantID // pinned at the BFF for a single-tenant user; "" otherwise
			if err := saveConfig(cfg); err != nil {
				return fail(err)
			}
			clearActiveLogin(dc.DeviceCode)
			fmt.Fprintln(os.Stderr, "Authorized. Credentials saved.")
			selectTenantAfterLogin(cfg, tok)
			return exitOK
		}
		switch ec := errorCode(raw); ec {
		case "authorization_pending", "":
			// Still waiting (or a transient empty body) — keep polling.
			continue
		case "slow_down":
			interval += 5
		case "access_denied":
			return failCode(errors.New("authorization was denied"), exitForbidden)
		case "expired_token":
			return fail(errors.New("the device code expired before approval"))
		default:
			// A terminal approve-side failure surfaced through the poll
			// (ADR-062 §2): the approval was attempted but rejected — e.g.
			// approval_needs_app (finish first-login/MFA setup in the app),
			// login_failed. Report the real reason instead of polling to expiry.
			if msg := errorMessage(raw); msg != "" {
				return fail(fmt.Errorf("approval failed: %s", msg))
			}
			return fail(fmt.Errorf("approval failed (%s)", ec))
		}
	}
	return fail(errors.New("timed out waiting for approval"))
}

// selectTenantAfterLogin does client-side tenant selection (ADR-062 §2):
//   - session already pinned (single-membership user): report the active tenant.
//   - exactly one membership but unpinned: auto-pick and pin it.
//   - many memberships: list them and tell the user how to choose.
//   - none: nothing to do.
func selectTenantAfterLogin(cfg *Config, tok deviceTokenResp) {
	if tok.TenantID != "" {
		fmt.Fprintf(os.Stderr, "Active tenant: %s\n", tok.TenantID)
		return
	}
	switch len(tok.Tenants) {
	case 0:
		// No tenant memberships — leave the session unpinned.
	case 1:
		if err := pinTenant(cfg, tok.Tenants[0].ID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not set active tenant: %v\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "Active tenant: %s\n", tenantLabel(tok.Tenants[0]))
	default:
		fmt.Fprintln(os.Stderr, "\nYou belong to multiple tenants:")
		for _, t := range tok.Tenants {
			fmt.Fprintf(os.Stderr, "  %s\n", tenantLabel(t))
		}
		fmt.Fprintln(os.Stderr, "\nSelect one with:  realm-id config set tenant <id>")
		fmt.Fprintln(os.Stderr, "(or pass --tenant <id> per command)")
	}
}

func tenantLabel(t tenantInfo) string {
	if t.DisplayName != "" {
		return fmt.Sprintf("%s (%s)", t.ID, t.DisplayName)
	}
	return t.ID
}

// pinTenant re-pins the BFF session to tenantID via POST /switch-tenant (no
// re-login — ADR-031) and persists it as the CLI's active tenant. The session
// is unusable on the admin surface until pinned (the BFF refuses an unpinned
// session with tenant_required), so a failed switch is surfaced, not swallowed.
func pinTenant(cfg *Config, tenantID string) error {
	if cfg.SessionToken == "" {
		return errors.New("not logged in (run: realm-id auth login)")
	}
	st, raw, err := jsonRequest(http.MethodPost, cfg.bffURL()+"/switch-tenant",
		cfg.SessionToken, map[string]string{"tenant_id": tenantID}, nil)
	if err != nil {
		return err
	}
	if st >= 400 {
		if code := errorCode(raw); code != "" {
			return fmt.Errorf("switch-tenant failed: %s", code)
		}
		return fmt.Errorf("switch-tenant failed (status %d)", st)
	}
	cfg.Tenant = tenantID
	return saveConfig(cfg)
}

func authWhoami(cfg *Config) int {
	if cfg.SessionToken == "" {
		return failCode(errors.New("not logged in (run: realm-id auth login)"), exitForbidden)
	}
	status, raw, err := jsonRequest(http.MethodGet, cfg.bffURL()+"/me", cfg.SessionToken, nil, nil)
	if err != nil {
		return fail(err)
	}
	_, _ = os.Stdout.Write(raw)
	fmt.Fprintln(os.Stdout)
	return exitForStatus(status)
}

func authLogout(cfg *Config) int {
	if cfg.SessionToken != "" {
		_, _, _ = jsonRequest(http.MethodPost, cfg.bffURL()+"/logout", cfg.SessionToken, nil, nil)
	}
	cfg.SessionToken = ""
	if err := saveConfig(cfg); err != nil {
		return fail(err)
	}
	fmt.Fprintln(os.Stderr, "Logged out.")
	return exitOK
}

// ---- config ----

func cmdConfig(args []string) int {
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	if len(args) == 0 {
		return configList(cfg)
	}
	switch args[0] {
	case "list":
		return configList(cfg)
	case "get":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: realm-id config get <key>")
			return exitUsage
		}
		fmt.Fprintln(os.Stdout, configValue(cfg, args[1]))
		return exitOK
	case "set":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: realm-id config set <platform|tenant|bff_url|issuer_url> <value>")
			return exitUsage
		}
		switch args[1] {
		case "platform":
			cfg.Platform = args[2]
		case "tenant":
			// Tenant selection re-pins the live BFF session (ADR-062 §2);
			// pinTenant persists cfg.Tenant only on a successful switch.
			if err := pinTenant(cfg, args[2]); err != nil {
				return fail(err)
			}
			fmt.Fprintf(os.Stderr, "Active tenant: %s\n", args[2])
			return exitOK
		case "bff_url":
			cfg.BFFURL = args[2]
		case "issuer_url":
			cfg.IssuerURL = args[2]
		default:
			fmt.Fprintf(os.Stderr, "unknown config key %q\n", args[1])
			return exitUsage
		}
		if err := saveConfig(cfg); err != nil {
			return fail(err)
		}
		return exitOK
	default:
		fmt.Fprintln(os.Stderr, "usage: realm-id config <list|get|set>")
		return exitUsage
	}
}

func configValue(cfg *Config, key string) string {
	switch key {
	case "platform":
		return cfg.Platform
	case "tenant":
		return cfg.Tenant
	case "bff_url":
		return cfg.bffURL()
	case "issuer_url":
		return cfg.issuerURL()
	default:
		return ""
	}
}

func configList(cfg *Config) int {
	sessionState := ""
	if cfg.SessionToken != "" {
		sessionState = "<set>"
	}
	out := map[string]string{
		"platform":      cfg.Platform,
		"tenant":        cfg.Tenant,
		"bff_url":       cfg.bffURL(),
		"issuer_url":    cfg.issuerURL(),
		"session_token": sessionState,
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(os.Stdout, string(b))
	return exitOK
}

// ---- api (generic authenticated passthrough) ----

func cmdAPI(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: realm-id api <GET|POST|PATCH|DELETE> <path> [json-body]")
		return exitUsage
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	method := strings.ToUpper(args[0])
	path := args[1]
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var body any
	if len(args) >= 3 && args[2] != "" {
		if err := json.Unmarshal([]byte(args[2]), &body); err != nil {
			return failCode(fmt.Errorf("invalid JSON body: %w", err), exitUsage)
		}
	}
	status, raw, err := jsonRequest(method, cfg.bffURL()+path, cfg.SessionToken, body, nil)
	if err != nil {
		return fail(err)
	}
	_, _ = os.Stdout.Write(raw)
	fmt.Fprintln(os.Stdout)
	return exitForStatus(status)
}

// ---- http + errors ----

var httpClient = &http.Client{Timeout: 30 * time.Second}

// jsonRequest issues an HTTP request. If out != nil the response body is also
// decoded into it. Returns the status code and the raw response bytes.
func jsonRequest(method, url, bearer string, body any, out any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if out != nil && len(raw) > 0 {
		decodeBody(raw, out)
	}
	return resp.StatusCode, raw, nil
}

// decodeBody unmarshals a BFF success body into out. Native GoFr handlers
// (the device-flow endpoints) wrap their return value in a {"data":{…}}
// envelope (gofr http/response.Response), so peel that off when present and
// fall back to the bare body for raw/passthrough responses that don't wrap.
func decodeBody(raw []byte, out any) {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && len(env.Data) > 0 {
		if json.Unmarshal(env.Data, out) == nil {
			return
		}
	}
	_ = json.Unmarshal(raw, out)
}

// errorCode pulls {"error":{"code":...}} out of a BFF error body.
func errorCode(raw []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	return e.Error.Code
}

// errorMessage extracts the human-readable message from an error envelope, for
// surfacing approve-side failures to the user.
func errorMessage(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	return e.Error.Message
}

func exitForStatus(status int) int {
	switch {
	case status < 400:
		return exitOK
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return exitForbidden
	case status == http.StatusNotFound:
		return exitNotFound
	case status == http.StatusConflict:
		return exitConflict
	default:
		return exitErr
	}
}

func fail(err error) int { return failCode(err, exitErr) }

func failCode(err error, code int) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return code
}
