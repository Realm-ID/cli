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
//	               published keys (reuses verify-token's verifier).
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
	Detail string `json:"detail"`
}

type doctorReport struct {
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
		fmt.Fprintln(os.Stderr, "usage: realm-id doctor [--issuer <url>] [--realm <id>] [--output json|text]   (key from $REALM_ID_API_KEY)")
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
	if rep.Failed > 0 {
		return exitErr
	}
	return exitOK
}

func printDoctorText(r doctorReport) {
	w := os.Stdout
	for _, c := range r.Checks {
		fmt.Fprintf(w, "%-4s  %-13s %s\n", c.Status, c.Name, c.Detail)
	}
	fmt.Fprintf(w, "ran %d of %d checks: %d passed, %d failed, %d skipped\n", r.Ran, r.Total, r.Passed, r.Failed, r.Skipped)
}

func doctorChecks(issuer, realm, apiKey string, client *http.Client) doctorReport {
	rep := doctorReport{Issuer: issuer, Realm: realm, Checks: []doctorCheck{}}
	add := func(name, status, format string, a ...any) {
		rep.Checks = append(rep.Checks, doctorCheck{name, status, fmt.Sprintf(format, a...)})
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
		add("issuer_health", "FAIL", "%s unreachable: %v", issuer, err)
	} else {
		var hb struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(body, &hb)
		switch {
		case st != http.StatusOK:
			add("issuer_health", "FAIL", "GET /.well-known/health answered %d", st)
		case hb.Status != "UP":
			add("issuer_health", "FAIL", "health answered 200 but body status is %q (not UP): the datastore is not healthy", hb.Status)
		default:
			add("issuer_health", "PASS", "%s is UP", issuer)
		}
	}

	// 2. discovery
	if realm == "" {
		add("discovery", "SKIP", "%s", noRealm)
	} else if st, body, err := get(issuer + "/" + realm + "/.well-known/openid-configuration"); err != nil {
		add("discovery", "FAIL", "request failed: %v", err)
	} else if st != http.StatusOK {
		add("discovery", "FAIL", "GET /%s/.well-known/openid-configuration answered %d (wrong realm id?)", realm, st)
	} else {
		var d struct {
			Issuer  string `json:"issuer"`
			JWKSURI string `json:"jwks_uri"`
		}
		if json.Unmarshal(body, &d) != nil {
			add("discovery", "FAIL", "body is not JSON")
		} else if want := issuer + "/" + realm; strings.TrimRight(d.Issuer, "/") != want {
			add("discovery", "FAIL", "discovery issuer is %q, expected %q: tokens' iss will not match your configured base URL", d.Issuer, want)
		} else if d.JWKSURI == "" {
			add("discovery", "FAIL", "discovery document has no jwks_uri")
		} else {
			add("discovery", "PASS", "issuer %s, jwks_uri %s", d.Issuer, d.JWKSURI)
		}
	}

	// 3. jwks
	if realm == "" {
		add("jwks", "SKIP", "%s", noRealm)
	} else if keys, err := fetchJWKS(&hc, issuer+"/"+realm+"/.well-known/jwks.json"); err != nil {
		add("jwks", "FAIL", "%v", err)
	} else if len(keys) == 0 {
		add("jwks", "FAIL", "JWKS has no keys: no token from this realm can be verified")
	} else {
		bad := 0
		for _, k := range keys {
			if _, kerr := k.rsaKey(); kerr != nil || k.Kid == "" {
				bad++
			}
		}
		if bad > 0 {
			add("jwks", "FAIL", "%d of %d keys are not usable RSA keys with a kid", bad, len(keys))
		} else {
			add("jwks", "PASS", "%d RSA key(s) published", len(keys))
		}
	}

	// 4 + 5. api key
	if apiKey == "" {
		add("api_key", "SKIP", "REALM_ID_API_KEY not set")
		add("api_key_token", "SKIP", "REALM_ID_API_KEY not set")
	} else {
		var out struct {
			AccessToken string `json:"access_token"`
		}
		st, raw, err := jsonRequest(http.MethodPost, issuer+"/auth/login", "",
			map[string]string{"grant_type": "platform_api_key", "api_key": apiKey}, &out)
		switch {
		case err != nil:
			add("api_key", "FAIL", "exchange request failed: %v", err)
			add("api_key_token", "SKIP", "no token: the key exchange did not complete")
		case st/100 != 2:
			add("api_key", "FAIL", "issuer refused the key: %d %s", st, errorCode(raw))
			add("api_key_token", "SKIP", "no token: the key was refused")
		case out.AccessToken == "":
			add("api_key", "FAIL", "issuer answered %d with no access_token", st)
			add("api_key_token", "SKIP", "no token returned")
		default:
			add("api_key", "PASS", "key accepted (platform_api_key exchange %d)", st)
			if v := verifyToken(out.AccessToken, issuer, "", time.Now(), &hc); v.Valid {
				add("api_key_token", "PASS", "minted token verifies against %s", v.JWKSURL)
			} else {
				add("api_key_token", "FAIL", "minted token does not verify: %s", strings.Join(v.Errors, "; "))
			}
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
		default:
			rep.Skipped++
		}
	}
	return rep
}
