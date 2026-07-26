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
