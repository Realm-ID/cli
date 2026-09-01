package main

import (
	_ "embed"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// openapiYAML is the issuer's OpenAPI 3.0.3 contract, vendored from
// issuer/docs/swagger.yaml. The typed command tree (ADR-062 §1) is generated
// from it at startup so it stays in lockstep as the API evolves — re-sync with
// `go generate ./...` and rebuild.
//
//go:generate cp ../../../issuer/docs/swagger.yaml openapi.yaml
//go:embed openapi.yaml
var openapiYAML []byte

// ---- OpenAPI document (only the fields the command tree needs) ----

type oaDoc struct {
	Paths map[string]oaPath `yaml:"paths"`
}

type oaPath struct {
	Parameters []oaParam `yaml:"parameters"`
	Get        *oaOp     `yaml:"get"`
	Post       *oaOp     `yaml:"post"`
	Patch      *oaOp     `yaml:"patch"`
	Put        *oaOp     `yaml:"put"`
	Delete     *oaOp     `yaml:"delete"`
}

func (p oaPath) byMethod() map[string]*oaOp {
	m := map[string]*oaOp{}
	if p.Get != nil {
		m["GET"] = p.Get
	}
	if p.Post != nil {
		m["POST"] = p.Post
	}
	if p.Patch != nil {
		m["PATCH"] = p.Patch
	}
	if p.Put != nil {
		m["PUT"] = p.Put
	}
	if p.Delete != nil {
		m["DELETE"] = p.Delete
	}
	return m
}

type oaOp struct {
	Tags        []string  `yaml:"tags"`
	Summary     string    `yaml:"summary"`
	Parameters  []oaParam `yaml:"parameters"`
	RequestBody *struct {
		Required bool `yaml:"required"`
	} `yaml:"requestBody"`
}

type oaParam struct {
	Name string `yaml:"name"`
	In   string `yaml:"in"` // path | query | header
}

// ---- derived command model ----

// pathParam is a single `{...}` path segment plus how the CLI fills it.
type pathParam struct {
	Name string // raw spec name, e.g. "pid", "roleId", "id"
	Role string // "platform" | "tenant" | "" (explicit --<name> flag)
}

// queryParam is an `in: query` parameter exposed as a --<name> flag.
type queryParam struct {
	Name string
}

// command is one leaf of the generated tree: `realm-id <group...> <verb>`.
type command struct {
	Group   []string // e.g. ["platforms"] or ["admin", "platforms"]
	Verb    string   // list | describe | create | update | <action>
	Method  string   // GET | POST | PATCH | PUT
	Path    string   // /platforms/{id}/roles/{roleId}/rename
	Params  []pathParam
	Query   []queryParam
	HasBody bool
	Summary string
}

// actionVerbs maps a trailing static action segment to a CLI verb. Anything in
// this set is treated as an action on the preceding resource (not a sub-
// collection noun); the method disambiguates a couple of them.
func actionVerb(method, seg string) (string, bool) {
	switch seg {
	case "claim", "verify", "rename", "remove", "accept", "reject", "approve",
		"resolve", "enroll", "confirm", "rotate", "suspend", "unsuspend",
		// Added 2026-08-28. Each of these was previously ABSENT from this set,
		// which is precisely how it became a top-level resource: see the
		// package-level note on `listOnlyCollections`. The segment reads as the
		// verb on its parent collection, so no renaming of the segment itself
		// is needed — `…/service-accounts/{said}/deactivate` is
		// `service-accounts deactivate`.
		"deactivate", "delink", "disable", "enable", "hand-back", "import",
		"leave", "request", "reset-handle", "revoke", "revoke-all",
		"tenant-choice":
		return seg, true
	case "mine":
		return "list-mine", true
	case "pending":
		// GET /domains/pending is a FILTERED LIST, not an act — the same shape
		// as `/platforms/mine`, and named the same way, so `domains pending`
		// cannot be misread as "make these domains pending".
		return "list-pending", true
	case "config":
		// Method-aware, unlike the segments above: /platforms/{id}/config
		// serves BOTH a GET and a PATCH. Keying on the segment alone put them
		// on one verb, and because they carry the same number of path params
		// the collision tie-break in buildCommands could not separate them
		// either — it fell through to Go's randomized map iteration, so
		// `platforms set-config` bound to GET or PATCH per RUN.
		//
		// `isAction` calls this with an empty method purely to ask "is this a
		// verb segment?", which still answers true via the write branch.
		if method == "GET" {
			return "get-config", true
		}
		return "set-config", true
	case "role":
		return "set-role", true
	case "status":
		return "set-status", true
	case "owner":
		return "set-owner", true
	case "password":
		// Added 2026-09-01 with the spec 0.38.0 re-vendor (ADR-104). Same shape
		// as `role`/`status`/`owner` above: a PUT that REPLACES a named
		// sub-resource of the parent, so `PUT /me/password` is
		// `me set-password`.
		//
		// Exposing it is a review decision, not a side effect. It is the SELF
		// route — the caller changes their own password and must present
		// `current_password` unless none is set — so the CLI cannot use it to
		// act on anyone else, and an agent holding the session already holds
		// the authority it confers.
		return "set-password", true
	case "credentials":
		// Added 2026-09-01 (ADR-104). The ADMIN counterpart:
		// `PUT /tenants/{tid}/users/{uid}/credentials` is
		// `users set-credentials`.
		//
		// ⚠️ What it writes is an ASSERTION, not a proof — the credential
		// carries `must_change`, and the holder's next login answers
		// `403 password_must_change` until they replace it. An operator
		// reaching for this to "log in as" somebody does not get that, by
		// design, and the CLI must not describe it as a way in.
		return "set-credentials", true
	}
	return "", false
}

// isAction reports whether a trailing static segment is an action verb rather
// than a resource noun.
func isAction(seg string) bool {
	_, ok := actionVerb("", seg)
	return ok
}

// listOnlyCollections are resource collections with no item route — nothing in
// the spec ever writes `…/<seg>/{id}` for them — so the structural rule in
// `collectionSegments` cannot see them and they must be named here.
//
// This list and `actionVerb` are the two halves of a TOTAL classification of
// trailing static segments. A segment in neither is `unclassified`, which
// `deriveCommand` SKIPS (absent from the typed tree, still reachable via
// `realm-id api`) and `TestEverySpecSegmentIsClassified` fails on. That is the
// whole point: before 2026-08-28 an unrecognised segment silently became a
// top-level resource whose verb was `create`, so thirteen bogus groups reached
// a shipped binary without anyone deciding to ship them. The lists decay only
// by going RED, which forces the naming decision the spec re-vendor skipped.
var listOnlyCollections = map[string]bool{
	"audit-events":   true,
	"events":         true,
	"login-attempts": true,
	"notes":          true,
	"permissions":    true,
	"search":         true,
	"starter-roles":  true,
	"stats":          true,
}

// collectionSegments is DERIVED from the embedded spec, not hand-listed: a
// static segment that some path follows with a further segment (`/roles/{id}`,
// `/sessions/{id}`) is being used as a collection there, so it is a noun
// everywhere. That covers the great majority of collections and shrinks the
// hand-maintained half to `listOnlyCollections`.
//
// Computed once — the spec is embedded and immutable, and `buildCommands` must
// stay a pure function of it (TestBuildCommandsIsDeterministic).
var collectionSegments = sync.OnceValue(func() map[string]bool {
	segs := map[string]bool{}
	var doc oaDoc
	if err := yaml.Unmarshal(openapiYAML, &doc); err != nil {
		return segs // every segment becomes unclassified; the guard test goes red
	}
	for path := range doc.Paths {
		parts := splitSegs(path)
		// Every segment BUT the last: a segment is a collection where something
		// follows it, which is the whole signal.
		for i := 0; i+1 < len(parts); i++ {
			if !isParamSeg(parts[i]) {
				segs[parts[i]] = true
			}
		}
	}
	return segs
})

// isCollectionSeg reports whether a trailing static segment names a resource
// collection (so GET=list / POST=create applies to the segment itself).
func isCollectionSeg(seg string) bool {
	return collectionSegments()[seg] || listOnlyCollections[seg]
}

// trailingStaticSegments returns every distinct trailing static segment across
// the spec paths the CLI considers (i.e. those surviving skipPath), sorted.
// It is the subject list the classification guard walks; deriving it from the
// document is what makes that guard notice a re-vendor nobody reviewed.
func trailingStaticSegments() ([]string, error) {
	var doc oaDoc
	if err := yaml.Unmarshal(openapiYAML, &doc); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for path := range doc.Paths {
		if skipPath(path) {
			continue
		}
		parts := splitSegs(path)
		if len(parts) == 0 {
			continue
		}
		if last := parts[len(parts)-1]; !isParamSeg(last) {
			seen[last] = true
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

// unclassifiedSegments returns the trailing static segments that are neither a
// known action verb nor a collection noun. Non-empty means the typed tree is
// missing operations, and is a hard test failure rather than a silent
// invention — see listOnlyCollections.
func unclassifiedSegments() ([]string, error) {
	all, err := trailingStaticSegments()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range all {
		if !isAction(s) && !isCollectionSeg(s) {
			out = append(out, s)
		}
	}
	return out, nil
}

func isParamSeg(s string) bool { return strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") }
func paramName(s string) string {
	return strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}")
}

// skipPath drops surfaces that the typed tree deliberately doesn't expose:
// the bespoke auth flows (realm-id auth …), public discovery, and the /me
// profile (realm-id auth whoami).
func skipPath(path string) bool {
	switch {
	case strings.HasPrefix(path, "/auth/"):
		return true
	case path == "/me":
		return true
	case strings.Contains(path, "/.well-known/"):
		return true
	}
	return false
}

// revocablePaths are the DELETE surfaces ADR-085 §8 lets back through the
// ADR-062 §5 filter. Both are SOFT: they set `revoked_at` on a row that stays
// readable, and a replacement is one `create` away. §5 exists to keep
// irreversible acts out of a machine-driven binary until machine-2FA exists —
// and rotation is part of onboarding, not an irreversible act. Excluding it
// meant a partner who lost a key could not rotate from the CLI at all, which
// pushes them toward the worse workaround of never rotating.
//
// user-api-keys is included on the same reasoning: ADR-084 §9 and the partner
// guide both name revocation as the primary control for an end-user key, so a
// binary that cannot revoke one contradicts the advice we ship with it.
//
// Everything else stays filtered. The test in spec_test.go pins both halves.
func isRevocable(path string) bool {
	return strings.HasPrefix(path, "/platforms/") && strings.Contains(path, "/api-keys/") ||
		strings.Contains(path, "/user-api-keys/")
}

// skipBFFOnly drops surfaces that require a PLATFORM bearer escorting a user
// token (ADR-050's Authorization: platform + X-User-Token shape). This binary
// holds a USER token from the ADR-062 device flow and cannot produce one, so
// such a verb would generate cleanly, appear in `--help`, and 401 at runtime —
// which is worse than not existing, because the operator has no way to tell a
// missing capability from a broken one.
//
// Two operations today, both on the user-API-key collection:
//
//   - POST …/user-api-keys — the mint, put behind the escort by ADR-097 §E.
//   - PUT  …/user-api-keys/{id} — the ADR-100 D12 replace, put behind the SAME
//     escort for the same reason. It shares the mint's write schema and can
//     WIDEN a key (`uncapped` false→true, `org_scope` selected→all, a longer
//     TTL), so an unescorted update would be the mint's hole reached by two
//     calls instead of one.
//
// `GET` and `DELETE` on the same collection are NOT escorted and stay available
// — ADR-084 §9 and the partner guide both name revocation as the primary
// control for an end-user key, so a binary that cannot revoke one would
// contradict the advice shipped with it.
//
// ⚠️ THE METHOD IS PART OF THE TEST, and must stay so. It is tempting to
// simplify this to "any path containing /user-api-keys", and that would filter
// `DELETE …/user-api-keys/{id}` — the revoke — which is the same path shape as
// the PUT and must pass through. `spec_test.go` pins both directions.
//
// These are filtered here rather than removed from the spec: they still exist,
// for BFFs. If RealmID staff later want CLI minting or updating, the fix is for
// the CLI to obtain a base-realm platform token — separate work, not smuggled in
// through this list.
func skipBFFOnly(method, path string) bool {
	if method == "POST" && strings.HasSuffix(path, "/user-api-keys") {
		return true
	}
	return method == "PUT" && strings.Contains(path, "/user-api-keys/")
}

// skipDestructive enforces ADR-062 §5: no delete, no signing-key rotate, no
// suspend/unsuspend, no ownership/domain transfer (PUT …/owner). These are
// absent from the binary until machine-2FA exists. Key revocation is the one
// documented exemption (see isRevocable).
//
// # The ADR-097 §G scope-removal amendment is RETIRED, because its subject is
//
// A long note used to sit here justifying `realm-id scopes remove`: §5 read on
// its own terms would have filtered it (irreversible by its own spec text, with
// an opt-in `?dry_run` — the soft-gate shape §5 rejects), and it was exposed
// anyway by an explicit owner decision on 2026-08-25, recorded as an amendment
// to §5.
//
// ADR-100 D10 then deleted `POST /platforms/{id}/scopes/remove` outright:
// retiring a scope is self-healing once the partner supplies `role_permissions`
// at every token mint. There is no operation left for the amendment to cover, so
// §5 is back to having no exceptions beyond revocation. The reasoning is kept in
// `git log` and in `DECISIONS.md` rather than here, because a filter that
// documents an endpoint nobody can call stops being readable.
func skipDestructive(method, path string) bool {
	if method == "DELETE" {
		return !isRevocable(path)
	}
	if method == "PUT" && strings.HasSuffix(path, "/owner") {
		return true
	}
	switch {
	case strings.HasSuffix(path, "/signing-keys/rotate"),
		strings.HasSuffix(path, "/suspend"),
		strings.HasSuffix(path, "/unsuspend"):
		return true
	}
	return false
}

// nearestStaticBefore returns the closest static (non-param) segment strictly
// before index i, or "" if none.
func nearestStaticBefore(segs []string, i int) string {
	for j := i - 1; j >= 0; j-- {
		if !isParamSeg(segs[j]) {
			return segs[j]
		}
	}
	return ""
}

// deriveCommand maps a (method, path) to a (group, verb), or ok=false to skip.
// Rules (ADR-062 §1, resource→noun / method→verb):
//   - trailing {param}      → item op: GET=describe, PATCH/PUT=update
//   - trailing action verb  → action on the nearest preceding resource noun
//   - trailing collection   → collection: GET=list, POST=create
//   - anything else         → NO COMMAND (see the note on listOnlyCollections)
//
// The last rule is the 2026-08-28 fix. "Anything else" used to fall into the
// collection case, so an unrecognised trailing segment became a TOP-LEVEL
// resource whose verb was `create` — `realm-id revoke create` to revoke a
// service account, `realm-id import create` to import users. Thirteen such
// groups shipped. Skipping is the conservative default: the operation stays
// reachable through `realm-id api`, and `TestEverySpecSegmentIsClassified`
// turns the skip into a red build rather than a silent hole.
//
// /admin/* paths are grouped under the `admin` command.
func deriveCommand(method, path string) (group []string, verb string, ok bool) {
	raw := strings.Split(strings.Trim(path, "/"), "/")
	segs := make([]string, 0, len(raw))
	for _, s := range raw {
		if s != "" {
			segs = append(segs, s)
		}
	}
	if len(segs) == 0 {
		return nil, "", false
	}

	var prefix []string
	if segs[0] == "admin" {
		prefix = []string{"admin"}
		segs = segs[1:]
	}
	if len(segs) == 0 {
		return nil, "", false
	}

	last := segs[len(segs)-1]
	switch {
	case isParamSeg(last):
		resource := nearestStaticBefore(segs, len(segs)-1)
		if resource == "" {
			return nil, "", false
		}
		switch method {
		case "GET":
			verb = "describe"
		case "PATCH", "PUT":
			verb = "update"
		case "DELETE":
			// Only reachable for the ADR-085 §8 exemptions — skipDestructive
			// filters every other DELETE before this runs. `revoke`, not
			// `delete`: the row survives, and naming it delete would tell a
			// partner something false about what they just did.
			verb = "revoke"
		default:
			return nil, "", false
		}
		return append(prefix, resource), verb, true

	case isAction(last):
		resource := nearestStaticBefore(segs, len(segs)-1)
		if resource == "" {
			return nil, "", false
		}
		v, _ := actionVerb(method, last)
		return append(prefix, resource), v, true

	case !isCollectionSeg(last):
		// Neither a known action nor a collection noun. SKIP rather than
		// invent: this branch used to fall through to the collection case
		// below, which made the segment its own top-level resource with a
		// `create` verb (`realm-id revoke create`). An absent command is
		// recoverable via `realm-id api`; a confidently wrong one is not.
		// `TestEverySpecSegmentIsClassified` fails while any path is here.
		return nil, "", false

	default: // trailing static noun → collection
		switch method {
		case "GET":
			verb = "list"
		case "POST":
			verb = "create"
		case "PATCH", "PUT":
			verb = "update"
		default:
			return nil, "", false
		}
		return append(prefix, last), verb, true
	}
}

// classifyParam decides how a path param is filled: platform/tenant come from
// active context (or --platform/--tenant); everything else is a required
// --<name> flag. The "owning collection" (nearest static segment before the
// param) disambiguates a bare {id}.
func classifyParam(segs []string, i int) pathParam {
	name := paramName(segs[i])
	owner := nearestStaticBefore(segs, i)
	switch {
	case name == "pid" || (name == "id" && owner == "platforms"):
		return pathParam{Name: name, Role: "platform"}
	case name == "tid" || name == "tenantId" || (name == "id" && owner == "tenants"):
		return pathParam{Name: name, Role: "tenant"}
	default:
		return pathParam{Name: name, Role: ""}
	}
}

// buildCommands parses the embedded spec into the deduped command tree. On a
// (group, verb) collision (the inevitable result of flattening a hierarchical
// API — e.g. platform- vs tenant-scoped identity-providers), the variant with
// the fewest path params (the broadest scope) wins; the rest are returned in
// `dropped` and remain reachable via `realm-id api`.
func buildCommands() (cmds []command, dropped []command, err error) {
	var doc oaDoc
	if err := yaml.Unmarshal(openapiYAML, &doc); err != nil {
		return nil, nil, err
	}

	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	chosen := map[string]command{} // key = group/verb
	for _, path := range paths {
		if skipPath(path) {
			continue
		}
		pi := doc.Paths[path]
		segs := splitSegs(path)
		for method, op := range pi.byMethod() {
			if skipDestructive(method, path) || skipBFFOnly(method, path) {
				continue
			}
			group, verb, ok := deriveCommand(method, path)
			if !ok {
				continue
			}
			c := command{
				Group:   group,
				Verb:    verb,
				Method:  method,
				Path:    path,
				HasBody: op.RequestBody != nil,
				Summary: firstLine(op.Summary),
			}
			for i, s := range segs {
				if isParamSeg(s) {
					c.Params = append(c.Params, classifyParam(segs, i))
				}
			}
			for _, q := range append(append([]oaParam{}, pi.Parameters...), op.Parameters...) {
				if q.In == "query" {
					c.Query = append(c.Query, queryParam{Name: q.Name})
				}
			}

			key := strings.Join(group, " ") + "\x00" + verb
			if prev, dup := chosen[key]; dup {
				// Keep the broadest (fewest path params); drop the rest.
				if len(c.Params) < len(prev.Params) {
					dropped = append(dropped, prev)
					chosen[key] = c
				} else {
					dropped = append(dropped, c)
				}
				continue
			}
			chosen[key] = c
		}
	}

	for _, c := range chosen {
		cmds = append(cmds, c)
	}
	sort.Slice(cmds, func(i, j int) bool {
		gi, gj := strings.Join(cmds[i].Group, " "), strings.Join(cmds[j].Group, " ")
		if gi != gj {
			return gi < gj
		}
		return cmds[i].Verb < cmds[j].Verb
	})
	sort.Slice(dropped, func(i, j int) bool { return dropped[i].Path < dropped[j].Path })
	return cmds, dropped, nil
}

func splitSegs(path string) []string {
	raw := strings.Split(strings.Trim(path, "/"), "/")
	segs := make([]string, 0, len(raw))
	for _, s := range raw {
		if s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
