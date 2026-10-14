package main

// `realm-id verify-token <jwt>` — check a RealmID access token against the
// realm's published keys and say, in plain words, what each claim means.
//
// Facts this relies on, all read from the issuer (not assumed):
//   - Signing is RS256 only (issuer internal/signer: algRS256 is the single
//     algorithm; the JWKS publishes kty=RSA, alg=RS256).
//   - `iss` is "<issuer base>/<realm id>" (internal/tokens: verifiers check
//     the iss PREFIX; httpapi.extractRealmID takes the remainder as the realm).
//   - The realm's keys are served at GET /{realm}/.well-known/jwks.json
//     (docs/swagger.yaml, tag JWKS).
//   - The claim set is internal/tokens.Claims plus partner custom claims.
//
// SECURITY. The token is untrusted input, so its `iss` must never decide
// where the keys come from: a forged token naming its own host would then
// verify against the forger's key. The JWKS base is the CONFIGURED issuer
// (--issuer, $REALM_ID_ISSUER, config, default) and a token whose iss is not
// under it is refused before any network call.

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

type claimLine struct {
	Name    string `json:"name"`
	Value   any    `json:"value"`
	Meaning string `json:"meaning"`
}

type verifyResult struct {
	helperEnvelope
	Verified   bool           `json:"signature_verified"`
	NotChecked []string       `json:"not_checked"`
	JWKSURL    string         `json:"jwks_url,omitempty"`
	Header     map[string]any `json:"header,omitempty"`
	// Claims are listed even for a token that failed, so a developer can see
	// WHY; they are only trustworthy when signature_verified is true.
	Claims []claimLine `json:"claims,omitempty"`
}

// claimMeanings: one plain-words line per claim the issuer mints
// (internal/tokens.Claims). Wording follows the field comments there.
var claimMeanings = map[string]string{
	"iss":           "Issuer: the realm that minted this token (issuer URL + realm id).",
	"sub":           "Subject: the user's per-org membership id (one row per person per tenant), not a person id. Key on (tenant_id, sub).",
	"aud":           "Audience: who the token was minted for. Your service must reject a token whose aud is not its own.",
	"iat":           "Issued at: when the token was minted.",
	"nbf":           "Not before: the token is not valid until this time.",
	"exp":           "Expiry: the token is not valid after this time.",
	"jti":           "Token id: a fresh unique id for this one token (not the session id; see sid).",
	"role":          "RealmID role name of the subject in that tenant. On the direct-bearer path this is the lookup key the issuer uses to resolve the permission set; the permissions themselves come from the database per request, not from the token.",
	"domain":        "Routing domain of the platform (ADR-064). Set by the issuer itself, for display/routing only; not authoritative (aud is the isolation key).",
	"sid":           "Session id: the session this token belongs to; revoking the session revokes the token. Absent on integration tokens.",
	"azp":           "Authorized party: the credential or app the token was minted for (e.g. apikey:<fingerprint> for a key-bootstrapped login).",
	"tenant_id":     "Tenant (org) this token acts in.",
	"mfa_at":        "When the subject last passed MFA in this session (drives step-up freshness checks).",
	"amr":           "Authentication methods used to establish the token (RFC 8176), e.g. a provider name, api_key, mfa.",
	"token_class":   "Kind of credential: platform (partner-broker token) or integration (cross-realm mint). Absent on an ordinary user token.",
	"scope":         "Granted authority: space-separated opaque scopes defined by the partner's own vocabulary (ADR-097).",
	"product_roles": "The partner's own role name(s) for the subject, carried by RealmID but never interpreted.",
	"events":        "Security-event claim: present only on logout/event tokens, never on an access token.",
}

func claimMeaning(name string, v any) string {
	m, ok := claimMeanings[name]
	if !ok {
		return "Custom claim supplied by the partner (custom_claims); RealmID does not interpret it."
	}
	switch name {
	case "iat", "nbf", "exp", "mfa_at":
		if f, ok := v.(float64); ok {
			m += " (" + time.Unix(int64(f), 0).UTC().Format(time.RFC3339) + ")"
		}
	}
	return m
}

func b64dec(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

func cmdVerifyToken(args []string) int {
	h, err := parseHelperArgs(args, "issuer", "aud", "output")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUsage
	}
	if len(h.pos) != 1 {
		fmt.Fprintln(os.Stderr, "usage: realm-id verify-token [--issuer <url>] [--aud <expected>] [--output json|text] <jwt|->")
		return exitUsage
	}
	token := h.pos[0]
	if token == "-" {
		b, _ := io.ReadAll(os.Stdin)
		token = strings.TrimSpace(string(b))
	}
	base := h.flags["issuer"]
	if base == "" {
		cfg, cerr := loadConfig()
		if cerr != nil {
			return fail(cerr)
		}
		base = cfg.issuerURL()
	}

	res := verifyToken(token, base, h.flags["aud"], time.Now(), http.DefaultClient)
	if wantJSON(h.flags["output"]) {
		writeJSON(res)
	} else {
		printVerifyText(res)
	}
	if !res.OK {
		return exitErr
	}
	return exitOK
}

func printVerifyText(r verifyResult) {
	w := os.Stdout
	if r.OK {
		fmt.Fprintln(w, "VALID  signature verified against "+r.JWKSURL)
	} else {
		fmt.Fprintln(w, "INVALID")
		for _, e := range r.Errors {
			fmt.Fprintln(w, "  - "+e.Code+": "+e.Message)
		}
		if len(r.Claims) > 0 {
			fmt.Fprintln(w, "  (claims below are UNVERIFIED)")
		}
	}
	for _, c := range r.Claims {
		v, _ := json.Marshal(c.Value)
		fmt.Fprintf(w, "%-14s %s\n    %s\n", c.Name, v, c.Meaning)
	}
	for _, n := range r.NotChecked {
		fmt.Fprintln(w, "not checked: "+n)
	}
}

// verifyToken does the work; now and client are injected so tests are exact.
func verifyToken(token, issuerBase, wantAud string, now time.Time, client *http.Client) verifyResult {
	res := verifyResult{helperEnvelope: newEnvelope("verify-token"), NotChecked: []string{
		"revocation: the CLI cannot see session state, so a revoked or demoted session still verifies until the token expires",
	}}
	if wantAud == "" {
		res.NotChecked = append([]string{"aud: no --aud given, so the audience was not compared (your service must do this)"}, res.NotChecked...)
	}
	bad := func(code, format string, a ...any) verifyResult {
		res.Errors = append(res.Errors, helperError{Code: code, Message: fmt.Sprintf(format, a...)})
		return res
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return bad("malformed", "a JWT has 3 dot-separated parts, got %d", len(parts))
	}
	hb, e1 := b64dec(parts[0])
	pb, e2 := b64dec(parts[1])
	sig, e3 := b64dec(parts[2])
	if e1 != nil || e2 != nil || e3 != nil {
		return bad("malformed", "a part is not base64url")
	}
	var claims map[string]any
	if json.Unmarshal(hb, &res.Header) != nil || json.Unmarshal(pb, &claims) != nil {
		return bad("malformed", "header or payload is not a JSON object")
	}
	names := make([]string, 0, len(claims))
	for k := range claims {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		res.Claims = append(res.Claims, claimLine{Name: k, Value: claims[k], Meaning: claimMeaning(k, claims[k])})
	}

	if alg, _ := res.Header["alg"].(string); alg != "RS256" {
		return bad("wrong_algorithm", "alg is %q; RealmID signs with RS256 only, so this token did not come from RealmID", alg)
	}
	iss, _ := claims["iss"].(string)
	prefix := strings.TrimRight(issuerBase, "/") + "/"
	realm := strings.TrimPrefix(iss, prefix)
	if iss == "" || !strings.HasPrefix(iss, prefix) || realm == "" || strings.Contains(realm, "/") {
		return bad("wrong_issuer", "iss %q is not <%s>/<realm id>; refusing to fetch keys from a host the token names (use --issuer to point at another issuer)", iss, strings.TrimRight(issuerBase, "/"))
	}

	res.JWKSURL = prefix + realm + "/.well-known/jwks.json"
	key, err := fetchRealmKey(client, res.JWKSURL, headerString(res.Header, "kid"))
	if err != nil {
		var ce *codedError
		if errors.As(err, &ce) {
			return bad(ce.code, "%s", ce.msg)
		}
		return bad("jwks_fetch_failed", "%v", err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return bad("bad_signature", "signature does not verify against the realm's key")
	}
	res.Verified = true

	// Access-token-only, mirroring the issuer's tokens.VerifyOptions.AccessOnly
	// (internal/tokens/tokens.go): a typ outside {JWT, at+jwt, application/at+jwt}
	// (absent refused), or an `events` claim (a security event token).
	switch strings.ToLower(headerString(res.Header, "typ")) {
	case "jwt", "at+jwt", "application/at+jwt":
	default:
		bad("not_an_access_token", "header typ is %q; an access token is typ JWT or at+jwt", headerString(res.Header, "typ"))
	}
	if _, has := claims["events"]; has {
		bad("not_an_access_token", "carries an events claim: a security event token, not an access token")
	}
	// The issuer's internal challenge tokens (authsvc mintBaseJWT: MFA challenge
	// "auth:mfa:challenge", session-revoke "auth:sessions:revoke") are signed by
	// the same realm key with typ JWT, so the AccessOnly rule above does not
	// catch them. They are single-purpose and must never be used as a bearer.
	if sub, _ := claims["sub"].(string); sub == "authsvc" {
		if sc, _ := claims["scope"].(string); strings.HasPrefix(sc, "auth:") {
			bad("not_an_access_token", "this is an internal RealmID challenge token (scope %q), not an access token", sc)
		}
	}

	if exp, ok := claims["exp"].(float64); !ok {
		bad("malformed", "no exp claim")
	} else if now.Unix() >= int64(exp) {
		bad("expired", "exp %s is in the past", time.Unix(int64(exp), 0).UTC().Format(time.RFC3339))
	}
	if nbf, ok := claims["nbf"].(float64); ok && now.Unix() < int64(nbf) {
		bad("not_yet_valid", "nbf %s is in the future", time.Unix(int64(nbf), 0).UTC().Format(time.RFC3339))
	}
	if wantAud != "" {
		if aud, _ := claims["aud"].(string); aud != wantAud {
			bad("wrong_audience", "aud is %q, expected %q", aud, wantAud)
		}
	}
	res.OK = len(res.Errors) == 0
	return res
}

func headerString(h map[string]any, k string) string { s, _ := h[k].(string); return s }

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// codedError carries a stable error code beside its message, so callers
// report {code, message} instead of a code embedded in text.
type codedError struct{ code, msg string }

func (e *codedError) Error() string { return e.code + ": " + e.msg }

func coded(code, format string, a ...any) error {
	return &codedError{code: code, msg: fmt.Sprintf(format, a...)}
}

// fetchJWKS GETs and decodes a JWK Set.
func fetchJWKS(client *http.Client, url string) ([]jwk, error) {
	c := *client
	if c.Timeout == 0 {
		c.Timeout = 10 * time.Second
	}
	resp, err := c.Get(url)
	if err != nil {
		return nil, coded("jwks_fetch_failed", "%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, coded("jwks_fetch_failed", "GET %s answered %d", url, resp.StatusCode)
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, coded("jwks_fetch_failed", "body is not a JWK Set: %v", err)
	}
	return set.Keys, nil
}

func (k jwk) rsaKey() (*rsa.PublicKey, error) {
	if k.Kty != "RSA" {
		return nil, errors.New("not an RSA key")
	}
	n, e1 := b64dec(k.N)
	e, e2 := b64dec(k.E)
	if e1 != nil || e2 != nil || len(n) == 0 || len(e) == 0 {
		return nil, errors.New("bad n/e")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
}

func fetchRealmKey(client *http.Client, url, kid string) (*rsa.PublicKey, error) {
	keys, err := fetchJWKS(client, url)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if k.Kid == kid {
			pk, err := k.rsaKey()
			if err != nil {
				return nil, coded("jwks_fetch_failed", "key %q unusable: %v", kid, err)
			}
			return pk, nil
		}
	}
	return nil, coded("unknown_kid", "no key with kid %q in the realm's JWKS (%d keys); rotation not finished, or a forged token", kid, len(keys))
}
