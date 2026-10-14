package main

// `realm-id doctor` — check, from a partner backend's point of view, that the
// RealmID issuer it is configured against is usable. One line per check.
//
// The checks (always 5; ones that lack an input are SKIP with the reason):
//
//	issuer_health  GET /.well-known/health — the DB-aware probe. It answers 200
//	               even when degraded and carries the verdict in the BODY, so
//	               the body is asserted (status == "UP"), never the status code.
//	               (/.well-known/alive is GoFr's liveness route and proves
//	               nothing about the datastore.)
//	discovery      GET /{realm}/.well-known/openid-configuration; `issuer` must
//	               equal <issuer>/<realm> (a mismatch is the SDK's wrong_issuer)
//	               and `jwks_uri` must be present.
//	jwks           GET /{realm}/.well-known/jwks.json; non-empty, every key a
//	               usable RSA key.
//	api_key        $REALM_ID_API_KEY is accepted: POST /auth/login with
//	               grant_type=platform_api_key (the exchange every partner
//	               backend performs, and what the typed tree does).
//	api_key_token  the token that exchange minted verifies against the realm's
//	               published keys (reuses verify-token's verifier) and, when
//	               --realm is given, names THAT realm in its iss.
//
// Exit codes: 0 every check PASS; 1 any FAIL; 3 no FAIL but at least one SKIP
// (incomplete: "did not look" must never read as "all clear").
//
// Why the key is proven by the exchange and not by a GET: the spec has no
// read that is open to every platform key regardless of its permissions, and
// a 403 from a permission-gated read would be reported as a bad key. The
// exchange is the issuer's own acceptance decision, and verifying what it
// minted proves the key leads to a token the partner's verifier will accept.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // PASS | FAIL | SKIP
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail"`
}

type doctorReport struct {
	helperEnvelope
	Issuer  string        `json:"issuer"`
	Realm   string        `json:"realm,omitempty"`
	Total   int           `json:"total"`
	Ran     int           `json:"ran"` // PASS + FAIL
	Passed  int           `json:"passed"`
	Failed  int           `json:"failed"`
	Skipped int           `json:"skipped"`
	Checks  []doctorCheck `json:"checks"`
}

func cmdDoctor(args []string) int {
	h, err := parseHelperArgs(args, "issuer", "realm", "output")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUsage
	}
	if len(h.pos) != 0 {
		fmt.Fprintln(os.Stderr, "usage: realm-id doctor [--issuer <url>] [--realm <id>] [--output json|text]   (key from $REALM_ID_API_KEY)\n"+
			"exit: 0 all checks PASS, 1 any FAIL, 3 no FAIL but some SKIP (incomplete)")
		return exitUsage
	}
	issuer := h.flags["issuer"]
	if issuer == "" {
		cfg, cerr := loadConfig()
		if cerr != nil {
			return fail(cerr)
		}
		issuer = cfg.issuerURL()
	}
	realm := h.flags["realm"]
	if realm == "" {
		realm = envOr("REALM_ID_REALM", "")
	}

	rep := doctorChecks(strings.TrimRight(issuer, "/"), realm, envOr("REALM_ID_API_KEY", ""), http.DefaultClient)
	if wantJSON(h.flags["output"]) {
		writeJSON(rep)
	} else {
		printDoctorText(rep)
	}
	switch {
	case rep.Failed > 0:
		return exitErr
	case rep.Skipped > 0:
		return exitIncomplete
	}
	return exitOK
}

func printDoctorText(r doctorReport) {
	w := os.Stdout
	for _, c := range r.Checks {
		fmt.Fprintf(w, "%-4s  %-13s %s\n", c.Status, c.Name, c.Detail)
	}
	fmt.Fprintf(w, "ran %d of %d checks: %d passed, %d failed, %d skipped\n", r.Ran, r.Total, r.Passed, r.Failed, r.Skipped)
	if r.Failed == 0 && r.Skipped > 0 {
		fmt.Fprintln(w, "incomplete: nothing failed, but skipped checks were not run (exit 3)")
	}
}

func doctorChecks(issuer, realm, apiKey string, client *http.Client) doctorReport {
	rep := doctorReport{helperEnvelope: newEnvelope("doctor"), Issuer: issuer, Realm: realm, Checks: []doctorCheck{}}
	pass := func(name, format string, a ...any) {
		rep.Checks = append(rep.Checks, doctorCheck{Name: name, Status: "PASS", Detail: fmt.Sprintf(format, a...)})
	}
	skip := func(name, format string, a ...any) {
		rep.Checks = append(rep.Checks, doctorCheck{Name: name, Status: "SKIP", Detail: fmt.Sprintf(format, a...)})
	}
	failc := func(name, code, format string, a ...any) {
		rep.Checks = append(rep.Checks, doctorCheck{Name: name, Status: "FAIL", Code: code, Detail: fmt.Sprintf(format, a...)})
	}
	hc := *client
	if hc.Timeout == 0 {
		hc.Timeout = 10 * time.Second
	}
	get := func(url string) (int, []byte, error) {
		resp, err := hc.Get(url)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, b, nil
	}
	noRealm := "no realm id: pass --realm <id> (or set REALM_ID_REALM)"

	// 1. health
	if st, body, err := get(issuer + "/.well-known/health"); err != nil {
		failc("issuer_health", "issuer_unreachable", "%s unreachable: %v", issuer, err)
	} else {
		var hb struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(body, &hb)
		switch {
		case st != http.StatusOK:
			failc("issuer_health", "health_bad_status", "GET /.well-known/health answered %d", st)
		case hb.Status != "UP":
			failc("issuer_health", "health_degraded", "health answered 200 but body status is %q (not UP): the datastore is not healthy", hb.Status)
		default:
			pass("issuer_health", "%s is UP", issuer)
		}
	}

	// 2. discovery
	if realm == "" {
		skip("discovery", "%s", noRealm)
	} else if st, body, err := get(issuer + "/" + realm + "/.well-known/openid-configuration"); err != nil {
		failc("discovery", "discovery_unreachable", "request failed: %v", err)
	} else if st != http.StatusOK {
		failc("discovery", "discovery_bad_status", "GET /%s/.well-known/openid-configuration answered %d (wrong realm id?)", realm, st)
	} else {
		var d struct {
			Issuer  string `json:"issuer"`
			JWKSURI string `json:"jwks_uri"`
		}
		if json.Unmarshal(body, &d) != nil {
			failc("discovery", "discovery_malformed", "body is not JSON")
		} else if want := issuer + "/" + realm; strings.TrimRight(d.Issuer, "/") != want {
			failc("discovery", "issuer_mismatch", "discovery issuer is %q, expected %q: tokens' iss will not match your configured base URL", d.Issuer, want)
		} else if d.JWKSURI == "" {
			failc("discovery", "discovery_no_jwks_uri", "discovery document has no jwks_uri")
		} else {
			pass("discovery", "issuer %s, jwks_uri %s", d.Issuer, d.JWKSURI)
		}
	}

	// 3. jwks
	if realm == "" {
		skip("jwks", "%s", noRealm)
	} else if keys, err := fetchJWKS(&hc, issuer+"/"+realm+"/.well-known/jwks.json"); err != nil {
		failc("jwks", "jwks_fetch_failed", "%v", err)
	} else if len(keys) == 0 {
		failc("jwks", "jwks_empty", "JWKS has no keys: no token from this realm can be verified")
	} else {
		bad := 0
		for _, k := range keys {
			if _, kerr := k.rsaKey(); kerr != nil || k.Kid == "" {
				bad++
			}
		}
		if bad > 0 {
			failc("jwks", "jwks_unusable_keys", "%d of %d keys are not usable RSA keys with a kid", bad, len(keys))
		} else {
			pass("jwks", "%d RSA key(s) published", len(keys))
		}
	}

	// 4 + 5. api key (all requests through the injected client)
	if apiKey == "" {
		skip("api_key", "REALM_ID_API_KEY not set")
		skip("api_key_token", "REALM_ID_API_KEY not set")
	} else {
		var out struct {
			AccessToken string `json:"access_token"`
		}
		st, raw, err := jsonRequestWith(&hc, http.MethodPost, issuer+"/auth/login", "",
			map[string]string{"grant_type": "platform_api_key", "api_key": apiKey}, &out)
		switch {
		case err != nil:
			failc("api_key", "api_key_exchange_failed", "exchange request failed: %v", err)
			skip("api_key_token", "no token: the key exchange did not complete")
		case st/100 != 2:
			failc("api_key", "api_key_refused", "issuer refused the key: %d %s", st, errorCode(raw))
			skip("api_key_token", "no token: the key was refused")
		case out.AccessToken == "":
			failc("api_key", "api_key_no_token", "issuer answered %d with no access_token", st)
			skip("api_key_token", "no token returned")
		default:
			pass("api_key", "key accepted (platform_api_key exchange %d)", st)
			checkMintedToken(out.AccessToken, issuer, realm, &hc, failc, pass)
		}
	}

	for _, c := range rep.Checks {
		rep.Total++
		switch c.Status {
		case "PASS":
			rep.Passed++
			rep.Ran++
		case "FAIL":
			rep.Failed++
			rep.Ran++
			rep.Errors = append(rep.Errors, helperError{Code: c.Code, Message: c.Name + ": " + c.Detail})
		default:
			rep.Skipped++
		}
	}
	rep.OK = rep.Failed == 0 && rep.Skipped == 0
	return rep
}

// checkMintedToken verifies the token the key exchange minted. With a realm
// configured the token's iss must name THAT realm: a key for realm A passing
// a doctor run configured for realm B would be a false all-clear.
func checkMintedToken(tok, issuer, realm string, hc *http.Client,
	failc func(name, code, format string, a ...any), pass func(name, format string, a ...any)) {
	v := verifyToken(tok, issuer, "", time.Now(), hc)
	if realm != "" {
		var iss string
		for _, c := range v.Claims {
			if c.Name == "iss" {
				iss, _ = c.Value.(string)
			}
		}
		if want := issuer + "/" + realm; iss != want {
			failc("api_key_token", "realm_mismatch", "the key mints tokens for %q but the configured realm is %q", iss, want)
			return
		}
	}
	if v.OK {
		pass("api_key_token", "minted token verifies against %s", v.JWKSURL)
		return
	}
	code, msgs := "token_unverifiable", []string{}
	for _, e := range v.Errors {
		msgs = append(msgs, e.Code+": "+e.Message)
	}
	failc("api_key_token", code, "minted token does not verify: %s", strings.Join(msgs, "; "))
}
