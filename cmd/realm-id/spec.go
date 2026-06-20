package main

import (
	_ "embed"
	"sort"
	"strings"

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
	case "claim", "verify", "rename", "accept", "reject", "approve",
		"resolve", "enroll", "confirm", "rotate", "suspend", "unsuspend":
		return seg, true
	case "mine":
		return "list-mine", true
	case "config":
		return "set-config", true
	case "role":
		return "set-role", true
	case "status":
		return "set-status", true
	case "owner":
		return "set-owner", true
	}
	return "", false
}

// isAction reports whether a trailing static segment is an action verb rather
// than a resource noun.
func isAction(seg string) bool {
	_, ok := actionVerb("", seg)
	return ok
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

// skipDestructive enforces ADR-062 §5: no delete, no signing-key rotate, no
// suspend/unsuspend, no ownership/domain transfer (PUT …/owner). These are
// absent from the binary until machine-2FA exists.
func skipDestructive(method, path string) bool {
	if method == "DELETE" {
		return true
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
//   - trailing static noun  → collection: GET=list, POST=create
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
			if skipDestructive(method, path) {
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
