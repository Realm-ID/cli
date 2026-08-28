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

// TestScopeRemoveIsGoneAndRenameSurvives — ADR-100 D10 deleted
// `POST /platforms/{id}/scopes/remove` outright, so the CLI must not generate a
// verb for it.
//
// This replaces TestScopeRemoveIsExposedAsScopesRemove, which pinned the
// opposite: the ADR-062 §5 amendment of 2026-08-25 that exposed a bulk,
// irreversible operation on an explicit owner decision. That decision is
// SUPERSEDED, not reversed — the endpoint it covered no longer exists anywhere,
// because retiring a scope is self-healing once the partner supplies
// `role_permissions` at every token mint. §5 is back to having no exceptions
// beyond revocation.
//
// ⚠️ THE ABSENCE ALONE PROVES NOTHING. A tree that failed to load has no
// `remove` verb either. `scopes rename` is the positive control: the sibling
// operation on the same collection, which survives, and whose presence is what
// makes the absence mean something.
func TestScopeRemoveIsGoneAndRenameSurvives(t *testing.T) {
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

	if renameCmd == nil {
		t.Fatal("`scopes rename` is missing — the spec failed to load, so this test inspected nothing")
	}
	if removeCmd != nil {
		t.Errorf("`scopes remove` generated from %s %s — the endpoint was DELETED by ADR-100 D10, "+
			"so a vendored spec still carrying it is a stale re-vendor, not a filter question",
			removeCmd.Method, removeCmd.Path)
	}

	// §5's own rules must still bind. The retired amendment covered exactly one
	// operation; if it ever comes back it comes back in the ADR, not here.
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
			t.Errorf("%s %s must still be filtered by §5", m, p)
		}
	}
}

// TestUserAPIKeyUpdateIsFilteredButRevokeSurvives — ADR-100 D12's `PUT` carries
// the same ADR-097 §E escort as the mint, so this binary cannot call it.
//
// The two halves are the whole test, because the PUT and the DELETE are the SAME
// PATH SHAPE — `…/user-api-keys/{id}`. A filter written as a path prefix would
// catch both and silently remove `realm-id user-api-keys revoke`, which ADR-084
// §9 and the partner guide both name as the primary control for an end-user key.
func TestUserAPIKeyUpdateIsFilteredButRevokeSurvives(t *testing.T) {
	const coll = "/tenants/{tid}/users/{uid}/user-api-keys"
	if !skipBFFOnly("PUT", coll+"/{id}") {
		t.Error("the ADR-100 update must be filtered: it shares the mint's escort and can WIDEN a key")
	}
	if skipBFFOnly("DELETE", coll+"/{id}") {
		t.Error("the revoke must NOT be filtered — same path shape, different method, and it is " +
			"the control the docs tell operators to use")
	}

	cmds, _, err := buildCommands()
	if err != nil {
		t.Fatalf("buildCommands: %v", err)
	}
	var sawUpdate, sawRevoke bool
	for _, c := range cmds {
		if !strings.Contains(c.Path, "/user-api-keys") {
			continue
		}
		switch c.Method {
		case "PUT":
			sawUpdate = true
		case "DELETE":
			sawRevoke = true
		}
	}
	if sawUpdate {
		t.Error("`user-api-keys update` generated — it would appear in --help and 401 at runtime, " +
			"which is worse than not existing")
	}
	// The positive control: without it, a tree that failed to load passes above.
	if !sawRevoke {
		t.Fatal("`user-api-keys revoke` is missing — either the spec failed to load or the " +
			"filter widened to a path match, taking the revoke with it")
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
// The thirteen MIS-DERIVED groups this list used to carry — `deactivate`,
// `delink`, `disable`, `enable`, `hand-back`, `import`, `leave`, `pending`,
// `request`, `reset-handle`, `revoke`, `revoke-all`, `tenant-choice` — are gone
// as of 2026-08-28. They existed because `deriveCommand` treated any trailing
// static segment `actionVerb` did not name as a collection noun, so the segment
// became a top-level resource whose verb was `create`: `realm-id revoke create`
// revoked a service account. They are pinned negatively by
// `TestActionSegmentsDeriveOnTheirParentResource`, so a regression fails there
// with a diagnosis rather than only here with "a group disappeared".
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

		// Parents of a trailing action segment, reached only that way. They are
		// resource groups like any other; what made them absent before
		// 2026-08-28 was the mis-derivation, not a decision.
		"contacts": true, "me": true, "memberships": true, "sessions": true,
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

// TestActionSegmentsDeriveOnTheirParentResource pins the shape ADR-062 §1 asks
// for — `<parent resource> <action>` — for every trailing action segment in the
// spec, not just the handful `actionVerb` happened to name.
//
// Until 2026-08-28, `deriveCommand` treated any trailing static segment absent
// from `actionVerb` as a COLLECTION NOUN, so the segment became its own
// top-level resource and the method chose the verb: `realm-id revoke create`
// revoked a service account and `realm-id import create` imported users.
// Fourteen commands were in that state.
//
// The table is exhaustive over the action segments the binary exposes, and it
// asserts the GROUP as well as the verb, because the group is the half that was
// wrong. The two `disable`/`enable` pairs are here for a second reason: under
// the old derivation both members of each pair derived the SAME (group, verb)
// — `disable create` — so the collision tie-break silently dropped one of each.
// Getting the group right is what makes all four reachable.
func TestActionSegmentsDeriveOnTheirParentResource(t *testing.T) {
	cases := []struct{ method, path, wantGroup, wantVerb string }{
		{"POST", "/tenants/{id}/users/{uid}/sessions/revoke", "sessions", "revoke"},
		{"POST", "/platforms/{id}/sessions/revoke-all", "sessions", "revoke-all"},
		{"GET", "/domains/pending", "domains", "list-pending"},
		{"POST", "/platforms/{id}/roles/{roleId}/disable", "roles", "disable"},
		{"POST", "/platforms/{id}/roles/{roleId}/enable", "roles", "enable"},
		{"POST", "/platforms/{id}/integrations/{iid}/disable", "integrations", "disable"},
		{"POST", "/platforms/{id}/integrations/{iid}/enable", "integrations", "enable"},
		{"POST", "/tenants/{id}/users/import", "users", "import"},
		{"POST", "/tenants/{id}/users/{uid}/hand-back", "users", "hand-back"},
		{"POST", "/tenants/{id}/service-accounts/{said}/reset-handle", "service-accounts", "reset-handle"},
		{"POST", "/tenants/{id}/service-accounts/{said}/revoke", "service-accounts", "revoke"},
		{"POST", "/tenants/{id}/service-accounts/{said}/deactivate", "service-accounts", "deactivate"},
		{"POST", "/tenants/{id}/users/{uid}/contacts/{contactId}/delink", "contacts", "delink"},
		{"POST", "/me/memberships/{tenantId}/leave", "memberships", "leave"},
		{"POST", "/me/tenant-choice", "me", "tenant-choice"},
		{"POST", "/platforms/{pid}/tenants/{tid}/sso-domains/{domain}/request", "sso-domains", "request"},
	}
	for _, tc := range cases {
		g, v, ok := deriveCommand(tc.method, tc.path)
		if !ok {
			t.Errorf("%s %s was skipped; want %q %q", tc.method, tc.path, tc.wantGroup, tc.wantVerb)
			continue
		}
		if got := strings.Join(g, " "); got != tc.wantGroup || v != tc.wantVerb {
			t.Errorf("%s %s → %q %q, want %q %q", tc.method, tc.path, got, v, tc.wantGroup, tc.wantVerb)
		}
	}

	// The commands must actually REACH the tree — deriveCommand agreeing in
	// isolation is what the v0.2.11 random-binding defect already proved is not
	// enough (only buildCommands could see that collision).
	tr, err := loadTree()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		c := find(t, tr, tc.wantGroup, tc.wantVerb)
		if c.Path != tc.path || c.Method != tc.method {
			t.Errorf("%s %s bound to %s %s", tc.wantGroup, tc.wantVerb, c.Method, c.Path)
		}
	}
	// And the mis-derived groups must be GONE, or the rename only added aliases.
	for _, gone := range []string{
		"deactivate", "delink", "disable", "enable", "hand-back", "import",
		"leave", "pending", "request", "reset-handle", "revoke", "revoke-all",
		"tenant-choice",
	} {
		if _, still := tr.byGroup[gone]; still {
			t.Errorf("mis-derived top-level group %q is still in the tree", gone)
		}
	}
}

// TestEverySpecSegmentIsClassified is the guard that makes the fix hold on the
// NEXT re-vendor.
//
// The old derivation had no notion of "I do not know what this segment is": an
// unrecognised trailing static segment silently became a top-level resource
// with a `create` verb, which is exactly how fourteen bogus commands reached a
// shipped binary without anyone deciding to ship them. `unclassifiedSegments`
// makes that state nameable, `deriveCommand` now SKIPS such a path instead of
// inventing a command for it, and this test fails when the set is non-empty.
//
// It is derived from the embedded spec, not hand-listed, so a re-vendor that
// introduces a new trailing segment goes red here and forces the naming
// decision — the thing that did not happen in the first place.
func TestEverySpecSegmentIsClassified(t *testing.T) {
	unknown, err := unclassifiedSegments()
	if err != nil {
		t.Fatalf("unclassifiedSegments: %v", err)
	}
	if len(unknown) > 0 {
		t.Errorf("trailing path segments the derivation cannot classify: %v\n"+
			"Each is neither a known action (add it to `actionVerb`) nor a collection "+
			"noun (add it to `listOnlyCollections` if it has no item route). Until then "+
			"the operation is ABSENT from the typed tree and reachable only via "+
			"`realm-id api`.", unknown)
	}

	// Positive control: the classifier must actually be looking at something.
	// An empty answer is the PASS condition above, so a broken parse, an empty
	// embed or an accidental early return would pass silently without this.
	all, err := trailingStaticSegments()
	if err != nil {
		t.Fatalf("trailingStaticSegments: %v", err)
	}
	if len(all) < 30 {
		t.Fatalf("only %d distinct trailing static segments found (%v) — the spec did not "+
			"load, so the assertion above inspected nothing", len(all), all)
	}
}

// TestUnclassifiedTrailingSegmentIsSkippedNotInvented covers the branch that
// cannot be reached from the CURRENT spec — by construction, since
// TestEverySpecSegmentIsClassified fails while any real path lands there.
//
// It is the whole behavioural change of the 2026-08-28 fix: the old default was
// "treat anything I do not recognise as a collection", which is how a segment
// nobody had classified became `realm-id <segment> create`. The new default is
// to produce NO command. A synthetic path is the only way to exercise it, and
// exercising it matters — without this test, deleting the guard clause passes
// the whole suite.
func TestUnclassifiedTrailingSegmentIsSkippedNotInvented(t *testing.T) {
	for _, method := range []string{"GET", "POST", "PATCH", "PUT"} {
		if _, _, ok := deriveCommand(method, "/tenants/{id}/frobnicate"); ok {
			g, v, _ := deriveCommand(method, "/tenants/{id}/frobnicate")
			t.Errorf("%s /tenants/{id}/frobnicate derived %q %q; an unclassified segment must "+
				"produce no command at all", method, strings.Join(g, " "), v)
		}
	}
	// Control: the same shape with a segment the classifier DOES know must
	// still derive, or the guard is just refusing everything.
	if _, _, ok := deriveCommand("GET", "/tenants/{id}/config"); !ok {
		t.Fatal("a known action segment stopped deriving — the guard is over-broad")
	}
	if _, _, ok := deriveCommand("GET", "/platforms/{id}/roles"); !ok {
		t.Fatal("a known collection segment stopped deriving — the guard is over-broad")
	}
}
