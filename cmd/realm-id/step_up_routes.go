package main

// The step-up route list: the BFF's own list of operations that need a fresh second
// factor, copied here so a key or workload session is refused locally, with nothing sent, and a
// person is stepped up. A row is a method, an issuer path pattern whose {placeholder} segments each
// match one segment, and an optional body condition. The copy mirrors the BFF's step-up list;
// TestStepUpRoutesMatchTheBFFList compares the two where the BFF's list is available.

import (
	"encoding/json"
	"strings"
)

type stepUpRoute struct {
	Method string
	Path   string
	// Field and Values are the optional body condition: the row matches only when the body's
	// Field holds one of Values ("*" matches any present value).
	Field  string
	Values []string
}

var stepUpRoutes = []stepUpRoute{
	{Method: "PUT", Path: "/tenants/{id}/owner"},
	{Method: "DELETE", Path: "/tenants/{id}"},
	{Method: "PATCH", Path: "/tenants/{id}", Field: "status", Values: []string{"deactivated"}},
	{Method: "POST", Path: "/platforms/{id}/api-keys"},
	{Method: "DELETE", Path: "/platforms/{id}/api-keys/{keyId}"},
	{Method: "POST", Path: "/platforms/{id}/signing-keys/split"},
	{Method: "POST", Path: "/tenants/{tid}/users/{uid}/user-api-keys"},
	{Method: "PUT", Path: "/tenants/{tid}/users/{uid}/user-api-keys/{id}"},
	{Method: "DELETE", Path: "/tenants/{tid}/users/{uid}/user-api-keys/{id}"},
	{Method: "PATCH", Path: "/platforms/{id}/config", Field: "email_otp_login_enabled", Values: []string{"true"}},
	{Method: "DELETE", Path: "/tenants/{tenantId}/users/{uid}/mfa"},
	{Method: "PUT", Path: "/tenants/{tid}/users/{uid}/credentials"},
	{Method: "PUT", Path: "/platforms/{id}/messaging/{channel}"},
	{Method: "DELETE", Path: "/platforms/{id}/messaging/{channel}"},
	{Method: "POST", Path: "/platforms/{id}/signing-keys/rotate"},
	{Method: "POST", Path: "/tenants/{id}/service-accounts/{said}/reset-handle"},
	{Method: "POST", Path: "/tenants/{tenantId}/users/{uid}/mfa/enroll"},
	{Method: "POST", Path: "/tenants/{tenantId}/users/{uid}/mfa/confirm"},
	{Method: "POST", Path: "/tenants/{id}/contact-verifications/{cvId}/approve"},
	{Method: "PATCH", Path: "/tenants/{id}/users/{uid}", Field: "email", Values: []string{"*"}},
	{Method: "PATCH", Path: "/tenants/{id}/users/{uid}", Field: "phone", Values: []string{"*"}},
}

// personOnlyRoutes are operations the ISSUER gates (a person plus a fresh TOTP, Ruling 64): the
// BFF carries no step-up row for them, so they are NOT in stepUpRoutes (the BFF-parity list). The
// CLI still refuses a key or workload session locally (exit 8, nothing sent) and steps a person
// through the issuer's 412.
var personOnlyRoutes = []stepUpRoute{
	{Method: "POST", Path: "/platforms/{id}/federation-bindings"},
	{Method: "DELETE", Path: "/platforms/{id}/federation-bindings/{bid}"},
}

// stepUpRequired reports whether a call to the BFF passthrough (an issuer path, no /api prefix, and
// the JSON body that will be sent, nil for none) matches a step-up route or a person-only route.
func stepUpRequired(method, path string, body []byte) bool {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	for _, r := range personOnlyRoutes {
		if strings.EqualFold(r.Method, method) && routePathMatches(path, r.Path) {
			return true
		}
	}
	for _, r := range stepUpRoutes {
		if !strings.EqualFold(r.Method, method) || !routePathMatches(path, r.Path) {
			continue
		}
		if r.Field == "" || bodyFieldMatches(body, r.Field, r.Values) {
			return true
		}
	}
	return false
}

// bodyJSONOf is the JSON a request body marshals to, nil for no body.
func bodyJSONOf(body any) []byte {
	if body == nil {
		return nil
	}
	b, _ := json.Marshal(body)
	return b
}

// routeIsUnconditional reports whether method+path match a row with no body condition.
func routeIsUnconditional(method, path string) bool {
	for _, r := range stepUpRoutes {
		if r.Field == "" && strings.EqualFold(r.Method, method) && routePathMatches(path, r.Path) {
			return true
		}
	}
	return false
}

// mgmtRoute reports whether method+path are platform-key mint or revoke, the two routes a
// platform_mgmt_api workload actor may use without a person.
func mgmtRoute(method, path string) bool {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	return (strings.EqualFold(method, "POST") && routePathMatches(path, "/platforms/{id}/api-keys")) ||
		(strings.EqualFold(method, "DELETE") && routePathMatches(path, "/platforms/{id}/api-keys/{keyId}"))
}

func routePathMatches(path, pattern string) bool {
	pp := strings.Split(strings.Trim(pattern, "/"), "/")
	rp := strings.Split(strings.Trim(path, "/"), "/")
	if len(pp) != len(rp) {
		return false
	}
	for i, seg := range pp {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			if rp[i] == "" {
				return false
			}
			continue
		}
		if seg != rp[i] {
			return false
		}
	}
	return true
}

// bodyFieldMatches reads a row's body condition the way the BFF does: the field name matches
// case-insensitively and the last duplicate wins; a missing or null field does not match; a value
// that is not a string (a boolean is read as "true"/"false" only by a row that lists one) or a body
// that is not valid JSON matches; "*" in the row's values matches any present value.
func bodyFieldMatches(body []byte, field string, values []string) bool {
	if len(body) == 0 {
		return false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return true
	}
	var raw json.RawMessage
	found := false
	dec := json.NewDecoder(strings.NewReader(string(body)))
	if _, err := dec.Token(); err == nil {
		for dec.More() {
			kt, kerr := dec.Token()
			if kerr != nil {
				return true
			}
			var v json.RawMessage
			if verr := dec.Decode(&v); verr != nil {
				return true
			}
			if k, ok := kt.(string); ok && strings.EqualFold(k, field) {
				raw, found = v, true
			}
		}
	}
	if !found || string(raw) == "null" {
		return false
	}
	for _, v := range values {
		if v == "*" {
			return true
		}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		var b bool
		if json.Unmarshal(raw, &b) != nil {
			return true
		}
		s = "false"
		if b {
			s = "true"
		}
		listsBool := false
		for _, v := range values {
			if v == "true" || v == "false" {
				listsBool = true
			}
		}
		if !listsBool {
			return true
		}
	}
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
