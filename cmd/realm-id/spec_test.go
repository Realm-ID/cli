package main

import (
	"strings"
	"testing"
)

// find returns the command for a `group… verb`, or fails.
func find(t *testing.T, tr *resourceTree, group, verb string) command {
	t.Helper()
	verbs, ok := tr.byGroup[group]
	if !ok {
		t.Fatalf("group %q not in tree", group)
	}
	c, ok := verbs[verb]
	if !ok {
		t.Fatalf("verb %q not under %q", verb, group)
	}
	return c
}

func TestDeriveCommand(t *testing.T) {
	cases := []struct {
		method, path string
		wantGroup    string
		wantVerb     string
		wantOK       bool
	}{
		{"GET", "/tenants", "tenants", "list", true},
		{"GET", "/tenants/{id}", "tenants", "describe", true},
		{"PATCH", "/tenants/{id}", "tenants", "update", true},
		{"POST", "/platforms", "platforms", "create", true},
		{"GET", "/platforms/mine", "platforms", "list-mine", true},
		{"POST", "/platforms/{id}/roles/{roleId}/rename", "roles", "rename", true},
		{"PATCH", "/tenants/{id}/users/{uid}/role", "users", "set-role", true},
		{"POST", "/domains/claim", "domains", "claim", true},
		{"GET", "/admin/platforms", "admin platforms", "list", true},
		{"POST", "/platforms/{id}/api-keys", "api-keys", "create", true},
		// ADR-085 §8 — key REVOCATION is exempt from the destructive filter.
		// Rotation is part of onboarding, not an irreversible act: a revoke is
		// soft and re-mintable, and without it a partner who loses a key cannot
		// rotate from the CLI at all. The verb is `revoke`, not `delete`, so the
		// binary never implies the row is gone.
		{"DELETE", "/platforms/{id}/api-keys/{keyId}", "api-keys", "revoke", true},
		{"DELETE", "/tenants/{tid}/users/{uid}/user-api-keys/{id}", "user-api-keys", "revoke", true},
		// destructive + non-CLI surfaces skip entirely
		{"DELETE", "/tenants/{id}", "", "", false},
		{"PUT", "/tenants/{id}/owner", "", "", false},
		{"POST", "/admin/platforms/{id}/signing-keys/rotate", "", "", false},
		{"POST", "/admin/platforms/{id}/suspend", "", "", false},
	}
	for _, tc := range cases {
		if skipPath(tc.path) || skipDestructive(tc.method, tc.path) {
			if tc.wantOK {
				t.Errorf("%s %s unexpectedly skipped", tc.method, tc.path)
			}
			continue
		}
		g, v, ok := deriveCommand(tc.method, tc.path)
		if ok != tc.wantOK {
			t.Errorf("%s %s ok=%v want %v", tc.method, tc.path, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if got := strings.Join(g, " "); got != tc.wantGroup || v != tc.wantVerb {
			t.Errorf("%s %s → %q %q, want %q %q", tc.method, tc.path, got, v, tc.wantGroup, tc.wantVerb)
		}
	}
}

func TestBuildCommandsTree(t *testing.T) {
	tr, err := loadTree()
	if err != nil {
		t.Fatal(err)
	}
	// Curated subset (ADR-062 §1) must all be present.
	for _, want := range []struct{ group, verb string }{
		{"platforms", "create"}, {"platforms", "update"},
		{"tenants", "list"}, {"tenants", "describe"},
		{"users", "list"}, {"users", "set-role"},
		{"invitations", "create"}, {"api-keys", "list"},
		{"federation-bindings", "list"}, {"origins", "list"},
		{"roles", "rename"}, {"audit-events", "list"},
		// ADR-085 §8 — the only two DELETEs the binary carries.
		{"api-keys", "revoke"}, {"user-api-keys", "revoke"},
	} {
		find(t, tr, want.group, want.verb)
	}
	// No destructive verbs anywhere, except the two soft revokes ADR-085 §8
	// exempts. Pinned as an exact allowlist rather than a `revoke`-name skip:
	// a future DELETE that happened to derive the verb `revoke` would otherwise
	// walk into the binary unnoticed.
	softRevokes := map[string]bool{"api-keys revoke": true, "user-api-keys revoke": true}
	for g, verbs := range tr.byGroup {
		for v, c := range verbs {
			if c.Method == "DELETE" && !softRevokes[g+" "+v] {
				t.Errorf("DELETE leaked into tree: %s %s", g, v)
			}
			if strings.Contains(v, "delete") || v == "rotate" || v == "suspend" {
				t.Errorf("destructive verb in tree: %s %s", g, v)
			}
		}
	}
}

func TestParamClassification(t *testing.T) {
	tr, err := loadTree()
	if err != nil {
		t.Fatal(err)
	}
	// /tenants/{id}/users/{uid}/role: id→tenant (context), uid→explicit flag.
	c := find(t, tr, "users", "set-role")
	roles := map[string]string{}
	for _, p := range c.Params {
		roles[p.Name] = p.Role
	}
	if roles["id"] != "tenant" {
		t.Errorf("users set-role {id} role = %q, want tenant", roles["id"])
	}
	if roles["uid"] != "" {
		t.Errorf("users set-role {uid} role = %q, want explicit flag", roles["uid"])
	}
	// /platforms/{id}/roles: id→platform.
	rc := find(t, tr, "roles", "list")
	if rc.Params[0].Role != "platform" {
		t.Errorf("roles list {id} role = %q, want platform", rc.Params[0].Role)
	}
}

// A read and a write on the same action path must not collapse onto one verb.
//
// `actionVerb` used to key on the trailing segment ALONE, so GET and PATCH on
// /platforms/{id}/config both derived `platforms set-config`. The collision
// tie-break keeps the variant with the fewest path params; these have the same
// count, so it fell through to whichever method `pi.byMethod()` yielded first —
// a Go map iteration, which is randomized. The command bound to GET or PATCH
// per RUN, and a run that bound GET accepted the operator's values and issued a
// read.
func TestActionVerbSeparatesReadFromWrite(t *testing.T) {
	cases := []struct{ method, path, wantGroup, wantVerb string }{
		{"GET", "/platforms/{id}/config", "platforms", "get-config"},
		{"PATCH", "/platforms/{id}/config", "platforms", "set-config"},
	}
	for _, tc := range cases {
		g, v, ok := deriveCommand(tc.method, tc.path)
		if !ok {
			t.Fatalf("%s %s was skipped", tc.method, tc.path)
		}
		if got := strings.Join(g, " "); got != tc.wantGroup || v != tc.wantVerb {
			t.Errorf("%s %s → %q %q, want %q %q", tc.method, tc.path, got, v, tc.wantGroup, tc.wantVerb)
		}
	}
	// The point is that they DIFFER. Asserting the two names above would still
	// pass if some later edit mapped both to the same new name.
	_, readVerb, _ := deriveCommand("GET", "/platforms/{id}/config")
	_, writeVerb, _ := deriveCommand("PATCH", "/platforms/{id}/config")
	if readVerb == writeVerb {
		t.Errorf("read and write on /platforms/{id}/config share verb %q", readVerb)
	}
}

// buildCommands must be a pure function of the spec.
//
// This is the GENERAL guard, and it is deliberately not a list of known
// collisions: it walks whatever the embedded spec contains, so a future path
// that collides is caught without anyone remembering to add a case. Ranging
// over a Go map is randomized per run, so repeating the build surfaces any
// binding that depends on that order.
func TestBuildCommandsIsDeterministic(t *testing.T) {
	type binding struct{ method, path string }

	first := map[string]binding{}
	cmds, _, err := buildCommands()
	if err != nil {
		t.Fatalf("buildCommands: %v", err)
	}
	for _, c := range cmds {
		first[strings.Join(c.Group, " ")+" "+c.Verb] = binding{c.Method, c.Path}
	}
	if len(first) == 0 {
		t.Fatal("no commands built — the guard would pass vacuously")
	}

	for i := 0; i < 50; i++ {
		got, _, err := buildCommands()
		if err != nil {
			t.Fatalf("buildCommands (run %d): %v", i, err)
		}
		if len(got) != len(cmds) {
			t.Fatalf("run %d built %d commands, first run built %d", i, len(got), len(cmds))
		}
		for _, c := range got {
			key := strings.Join(c.Group, " ") + " " + c.Verb
			want, ok := first[key]
			if !ok {
				t.Fatalf("run %d produced %q, absent from the first run", i, key)
			}
			if c.Method != want.method || c.Path != want.path {
				t.Fatalf("run %d: %q bound to %s %s, first run bound %s %s — build is order-dependent",
					i, key, c.Method, c.Path, want.method, want.path)
			}
		}
	}
}

// TestUserAPIKeyCreateIsFilteredButListAndRevokeSurvive pins ADR-097 §E's
// consequence for this binary.
//
// Minting a user API key requires a PLATFORM bearer escorting a user token
// (ADR-050's Authorization + X-User-Token shape). This CLI holds a USER token
// from the ADR-062 device flow and cannot produce one, so the verb would
// generate cleanly, appear in `--help`, and 401 at runtime — worse than absent,
// because the operator cannot tell a missing capability from a broken one.
//
// The subjects are DERIVED from the built tree, not listed: this asks the same
// buildCommands the binary runs. Every earlier test in this file that called
// deriveCommand in isolation could not have seen a filter at all — the same gap
// that hid the v0.2.11 random-verb-binding defect, where the collision was only
// visible to buildCommands.
//
// Both halves are asserted. "create is absent" alone is satisfied by a tree with
// no user-api-keys commands whatsoever — including one produced by a spec that
// failed to load — so list and revoke must be present for the absence to mean
// anything.
func TestUserAPIKeyCreateIsFilteredButListAndRevokeSurvive(t *testing.T) {
	cmds, _, err := buildCommands()
	if err != nil {
		t.Fatalf("buildCommands: %v", err)
	}
	if len(cmds) == 0 {
		t.Fatal("buildCommands produced NO commands; this test would have inspected nothing")
	}

	verbs := map[string]string{} // verb -> method, for the user-api-keys group
	for _, c := range cmds {
		if len(c.Group) == 1 && c.Group[0] == "user-api-keys" {
			verbs[c.Verb] = c.Method
		}
	}
	if _, present := verbs["create"]; present {
		t.Errorf("ADR-097 §E: `user-api-keys create` must be filtered — this binary cannot "+
			"satisfy the platform escort, so the verb would 401 at runtime. Got %v", verbs)
	}
	for _, want := range []string{"list", "revoke"} {
		if _, present := verbs[want]; !present {
			t.Errorf("`user-api-keys %s` must SURVIVE the filter (§E gates creation only, and "+
				"ADR-084 §9 names revocation as the primary control for an end-user key). Got %v",
				want, verbs)
		}
	}

	// The rule is method-and-path shaped, not name shaped: the platform-key
	// collection is a different resource and must be untouched.
	if !skipBFFOnly("POST", "/tenants/{tid}/users/{uid}/user-api-keys") {
		t.Error("skipBFFOnly must filter the user-api-keys mint")
	}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/tenants/{tid}/users/{uid}/user-api-keys"},
		{"DELETE", "/tenants/{tid}/users/{uid}/user-api-keys/{id}"},
		{"POST", "/platforms/{id}/api-keys"},
	} {
		if skipBFFOnly(tc.method, tc.path) {
			t.Errorf("skipBFFOnly must NOT filter %s %s", tc.method, tc.path)
		}
	}
}
