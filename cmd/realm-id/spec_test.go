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

// TestScopeRemoveIsFilteredButRenameSurvives pins ADR-062 §5 against the
// ADR-097 §G scope removal added in spec `0.32.0`.
//
// §5 states its rule as a property, not as its three examples: the verbs are
// absent so that "a credential handed to an agent CANNOT perform an irreversible
// action even if the agent tries", explicitly "stronger than a `--yes`
// soft-gate". `POST /platforms/{id}/scopes/remove` is irreversible by its own
// description, deletes a scope from every `permissions_cap` in the realm in one
// transaction, and under `on_empty=revoke` bulk-revokes every key the removal
// would uncap — keys this binary cannot re-mint, since ADR-097 §E filters the
// mint (see skipBFFOnly). Its `?dry_run=true` preview is opt-in, which is the
// soft-gate shape §5 rejects.
//
// Both halves are asserted. "remove is absent" alone is satisfied by a tree that
// failed to load, so `scopes rename` — the sibling operation on the same
// collection, which §5 does NOT reach because a rename is reversible by renaming
// back — must be PRESENT for the absence to carry any meaning.
func TestScopeRemoveIsExposedAsScopesRemove(t *testing.T) {
	// ADR-062 §5 Amendment (2026-08-25, owner decision): scope removal is
	// exposed despite being irreversible. This guard is the inverse of the one
	// it replaced, deliberately — the amendment is a DECISION, so what needs
	// pinning is that the decision still holds and that the op did not drift
	// back to the bogus `remove create` shape it derives as by default.
	cmds, _, err := buildCommands()
	if err != nil {
		t.Fatalf("buildCommands: %v", err)
	}
	if len(cmds) == 0 {
		t.Fatal("buildCommands produced NO commands; this test would have inspected nothing")
	}

	var removeCmd, renameCmd *command
	for i, c := range cmds {
		switch {
		case strings.HasSuffix(c.Path, "/scopes/remove"):
			removeCmd = &cmds[i]
		case strings.HasSuffix(c.Path, "/scopes/rename"):
			renameCmd = &cmds[i]
		}
	}

	// The sibling is the positive control: rename has generated since spec
	// 0.30.0, so its absence would mean the spec failed to load and every
	// assertion here proved nothing.
	if renameCmd == nil {
		t.Fatal("`scopes rename` is missing — the spec failed to load, so this test inspected nothing")
	}
	if removeCmd == nil {
		t.Fatal("POST /platforms/{id}/scopes/remove must be EXPOSED (ADR-062 §5 amendment, " +
			"2026-08-25). If you are re-filtering it, amend the ADR rather than this test.")
	}

	// `remove` must be an ACTION on `scopes`, not a resource of its own. Absent
	// from actionVerb it derives as the top-level `remove create`, which is the
	// same mis-derivation 13 existing commands already carry (see TODO).
	if got := strings.Join(removeCmd.Group, " "); got != "scopes" {
		t.Errorf("scope removal must group under `scopes`, got %q (verb %q) — "+
			"a bare `remove` group means actionVerb no longer names the segment", got, removeCmd.Verb)
	}
	if removeCmd.Verb != "remove" {
		t.Errorf("verb = %q, want \"remove\"", removeCmd.Verb)
	}
	if removeCmd.Method != "POST" {
		t.Errorf("method = %q, want POST", removeCmd.Method)
	}

	// The preview is the only surface that can report which keys a removal
	// would uncap (the 409 envelope carries no payload), so a build that drops
	// the flag leaves the operator unable to look before writing.
	var sawDryRun bool
	for _, q := range removeCmd.Query {
		if q.Name == "dry_run" {
			sawDryRun = true
		}
	}
	if !sawDryRun {
		t.Error("`scopes remove` must carry --dry-run: it is the ONLY way to learn the " +
			"`emptied` rows before writing")
	}

	if skipDestructive("POST", "/platforms/{id}/scopes/remove") {
		t.Error("skipDestructive must NOT filter the scope removal (ADR-062 §5 amendment)")
	}
	// The amendment is scoped to ONE operation. If it ever widens, it must widen
	// in the ADR, not by accident here.
	for _, p := range []string{
		"/platforms/{id}/signing-keys/rotate",
		"/platforms/{id}/suspend",
		"/tenants/{id}/owner",
	} {
		m := "POST"
		if strings.HasSuffix(p, "/owner") {
			m = "PUT"
		}
		if !skipDestructive(m, p) {
			t.Errorf("%s %s must still be filtered — the §5 amendment covers scope removal alone", m, p)
		}
	}
}

// TestTopLevelResourceGroupsAreReviewed fails when a spec re-vendor changes the
// set of resource groups the binary exposes.
//
// This is a deliberately HAND-MAINTAINED list, against this workspace's standing
// rule, because here the decay direction is the safe one. A derived subject list
// answers "is each thing we thought of still right?"; this answers the question
// that actually went unasked — "did the spec grow a command nobody looked at?"
// The only way it rots is by going RED, which forces exactly the review it
// exists to force. It also catches removals, which are more dangerous than
// additions: a partner's script breaks with no signal from a chore commit.
//
// Recorded so the list is not mistaken for an endorsement: the entries marked
// below are MIS-DERIVED. `deriveCommand` treats a trailing static segment as a
// collection noun unless `actionVerb` names it, so every action segment absent
// from that set becomes a top-level resource whose verb is `create` — you run
// `realm-id revoke create` to revoke a service account. Fourteen commands are in
// this state; it predates the ADR-097 work and is tracked in TODO.md. Each needs
// its own naming decision, so they are pinned here as-is rather than quietly
// renamed.
func TestTopLevelResourceGroupsAreReviewed(t *testing.T) {
	want := map[string]bool{
		"admin events": true, "admin notes": true, "admin platforms": true,
		"admin search": true, "admin stats": true,
		"api-keys": true, "audit-events": true, "contact-drift-reviews": true,
		"contact-verifications": true, "domains": true, "federation-bindings": true,
		"identity-providers": true, "integration-installations": true,
		"integrations": true, "invitations": true, "login-attempts": true,
		"mfa": true, "origins": true, "permissions": true, "platforms": true,
		"roles": true, "scopes": true, "service-accounts": true,
		"signing-keys": true, "sources": true, "sso-domains": true,
		"starter-roles": true, "stats": true, "tenants": true,
		"user-api-keys": true, "users": true,

		// MIS-DERIVED action segments (see the doc comment). Not endorsed.
		"deactivate": true, "delink": true, "disable": true, "enable": true,
		"hand-back": true, "import": true, "leave": true, "pending": true,
		"request": true, "reset-handle": true, "revoke": true,
		"revoke-all": true, "tenant-choice": true,
	}

	tr, err := loadTree()
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.groups) == 0 {
		t.Fatal("no groups built — the guard would pass vacuously")
	}

	got := map[string]bool{}
	for _, g := range tr.groups {
		got[g] = true
		if !want[g] {
			t.Errorf("NEW resource group %q reached the binary. Diff the command tree, decide "+
				"whether it should be exposed at all (ADR-062 §5) and whether it is named "+
				"correctly, then add it here.", g)
		}
	}
	for g := range want {
		if !got[g] {
			t.Errorf("resource group %q DISAPPEARED from the binary. A removal breaks callers "+
				"silently; confirm it was intended, then drop it from this list.", g)
		}
	}
}
