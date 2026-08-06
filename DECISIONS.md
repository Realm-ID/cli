# Decisions — Realm-ID/cli

Rationale log for the `realm-id` CLI. WHAT-shipped lives in git/CHANGELOG; this
file records WHY. See the root `Realm-ID/project` DECISIONS.md for cross-cutting
context.

## 2026-08-06 — re-vendor the spec to 0.24.0; `platforms describe` costs a `cp`

The vendored `openapi.yaml` was pinned at spec `0.20.0` while the issuer had
reached `0.24.0`. Re-vendored (the `//go:generate` line in `spec.go` is the
whole procedure) and reviewed the resulting command-tree diff rather than just
the endpoint that motivated it — a re-vendor pulls in four versions of drift.

**Net effect: three commands added, none removed, none renamed.**
`platforms describe` (`GET /platforms/{id}`), `admin platforms describe`
(`GET /admin/platforms/{id}`), and `invitations accept` (the ADR-095 endpoint
that shipped in issuer `v0.83.0`). The diff was taken between two builds that
BOTH carried the `set-config` fix below, so it isolates spec drift.

**Why this closes a "needs an SDK wrapper" item.** `sdk/TODO.md` recorded
`GET /platforms/{id}` as owed to the partner SDKs because "it is the read the
CLI's `platforms describe` needs". The CLI does not use the Go SDK at all —
`go.mod` requires only `gopkg.in/yaml.v3`, and requests go through its own
`newRequest`. Commands are DERIVED from the embedded spec at runtime by
`buildCommands`, and `deriveCommand` already maps a trailing `{param}` + GET to
`describe`. So the feature needed no SDK, no new resource type and no `SPEC.md`
change — it needed a file copy.

**Consequence for the SDK item:** with its only named consumer served, adding a
partner-facing `platforms` resource to go/ts/java has no identified caller.
Recorded as closed-unless-asked in `sdk/TODO.md` rather than left open, because
an item that describes work nobody needs is indistinguishable from one that is
merely unstarted.

## 2026-08-06 — `platforms set-config` bound to GET or PATCH at random, per run

**RCA — a write command that sometimes performed a read**

**Symptom** — `realm-id platforms set-config` was bound to `GET
/platforms/{id}/config` on some invocations and `PATCH` on others, from the
SAME binary and the SAME embedded spec. Observed directly: eight consecutive
runs of one build printed the GET summary five times and the PATCH summary
three times. On a run that bound GET, the command accepts the operator's
config values, issues a read, and reports success — the change silently does
not happen. (That impact is read off the code path; it was not executed
against a live issuer.)

**Root cause** — `actionVerb` mapped a trailing path segment to a verb using
the SEGMENT ALONE, ignoring the HTTP method: `case "config": return
"set-config", true`. `/platforms/{id}/config` serves both a GET (issuer
v0.52.0) and a PATCH, so both derived the identical `(group, verb)` key
`platforms set-config`. `buildCommands` resolves such a collision by keeping
the variant with the FEWEST path params — but these two are the same path, so
their param counts are equal, the comparison `len(c.Params) < len(prev.Params)`
is false, and it keeps whichever arrived first. That order comes from ranging
`pi.byMethod()`, a Go map, whose iteration order is deliberately randomized.

**Why it wasn't caught** — three reinforcing reasons, and the third is the
general one:
1. `TestDeriveCommand` is a table of hand-picked `(method, path)` cases and
   nobody added a `/config` row. It could only ever cover the pairs someone
   thought of — the hand-maintained-subject-list failure, again.
2. Every test asserted `deriveCommand` in ISOLATION, where the bug is
   invisible: `deriveCommand("GET", …)` and `deriveCommand("PATCH", …)` each
   return a stable answer. The defect only exists in `buildCommands`, where the
   two answers COLLIDE, and nothing tested that function's output was stable.
3. Randomized failures do not look like failures. A 50/50 binding presents as
   "it worked yesterday", which reads as user error rather than a bug.

**Fix** — `actionVerb` is method-aware for `config`: `GET` → `get-config`,
writes → `set-config`. Verified this renames nothing else: `config` is the only
action segment in the whole spec carrying more than one non-DELETE method, and
the only other GET-bearing action paths (`/domains/resolve`, `/platforms/mine`)
already map to `resolve` / `list-mine`. `isAction` calls this with an empty
method purely to ask "is this a verb segment?" and still answers true.

**Prevention** — `TestBuildCommandsIsDeterministic` builds the command tree 51
times and fails if any `(group, verb)` binds to a different `(method, path)`
than the first run. It is deliberately NOT a list of known collisions: it walks
whatever the embedded spec contains, so a future colliding path is caught
without anyone remembering to add a case — which is exactly what did not happen
here. It also guards the re-vendor: a spec bump that introduces a collision now
fails the build instead of shipping a coin-flip. `t.Fatal` on an empty command
set stops it passing vacuously if the spec ever fails to parse.

**Not fixed here, deliberately** — the collision RESOLUTION is still silent for
genuinely different paths (the platform- vs tenant-scoped variants it was
written for). Those are deterministic, because they differ in param count, so
they are out of scope for this bug; the dropped variants stay reachable via
`realm-id api`.

## 2026-08-05 — service mode never worked, and the test was holding it that way

**RCA — `REALM_ID_API_KEY` typed commands always 401'd**

**Symptom** — every typed command run in Service mode (`REALM_ID_API_KEY=rk_live_…`)
failed against the issuer with `401 invalid bearer`. Only session mode
(device flow → BFF passthrough) had ever worked, so the CLI's entire
non-interactive path — the one the README recommends for a long provisioning
run, precisely because a device-login session expires mid-sequence — was dead.

**Root cause** — `resolveCredential` sent the raw `rk_live_…` as
`Authorization: Bearer`, on an in-code claim that "the issuer accepts it as a
platform credential". It does not, and never did: `requireAuth` runs the bearer
through `LocalVerifier.Verify`, which rejects anything that is not a 3-part JWT,
and `LookupByPresented` is reachable only from `/auth/login`, the user-api-key
exchange and the integration mint. The api key is a **bootstrap** credential
under the ADR-051 two-endpoint surface — it is exchanged for a platform JWT, it
is not itself a bearer. The CLI skipped the exchange.

Not an ADR-089 regression: this lane never held a refresh token.

**Why it wasn't caught** — `TestResolveCredential` asserted
`bearer == "rk_live_1"`. **The test encoded the implementation and therefore
protected the bug**: it passed for as long as service mode was broken, and would
have failed the moment someone fixed it. A test that restates what the code does
cannot discover that what the code does is wrong. There was also no test that
ever put a server on the other end of service mode, so nothing observed the 401.

**Fix** — `resolveCredential` now exchanges the key at
`POST /auth/login {grant_type: platform_api_key, api_key: …}` and bears the
returned `access_token`. It returns an error rather than an empty bearer when
the exchange fails, and carries the issuer's own response body — a bare "401"
leaves a user unable to tell a bad key from a bad URL.

The token is cached in a process global, not the config file. A CLI invocation
runs one command, so that is at most one exchange per run with nothing to expire
against. Persisting it was rejected: per ADR-089 this lane returns **no refresh
token** — the api key *is* the renewable credential — so writing the JWT to disk
would add a second bearer at rest and buy nothing, since re-exchanging costs one
request and cannot fail in a way that holding a stale token would fix.

**Prevention** — the replacement test asserts the CONTRACT against an httptest
server that behaves like the issuer: the exchange path, grant type and key, that
the bootstrap call carries **no** Authorization header (the key travels in the
body), that the resulting bearer is the JWT and explicitly **not** the raw key,
and that a second resolve does not re-exchange. Mutation-verified — restoring
the old behaviour fails it with the message naming the defect. A failure-path
test pins that a rejected key surfaces the issuer's body.

The README's "it uses that platform key" line is corrected in the same change;
it was the doc half of the same false premise.

**Still open** (`TODO.md`): the CLI cannot yet present the ADR-088 escort bearer
plus a forwarded `X-User-Token` for on-behalf calls, which is what "the CLI can
act as the BFF" needs. This fix makes service mode work for platform-authority
commands; it does not make the CLI a BFF.

## 2026-07-24 — Re-sync vendored spec for owner-required tenant create (ADR-073 Amendment C)

**Problem.** The issuer now requires an inline `owner` on `POST
/platforms/{pid}/tenants` (`owner_user_id` is `NOT NULL`, ADR-076) and accepts
optional bring-your-own `id` + `created_at`. The CLI's command tree is generated
from the vendored `openapi.yaml`, which still described the old ownerless body.

**Decision.** Re-ran `go generate` to re-vendor `issuer/docs/swagger.yaml`
(→ spec `0.15.0`). **No CLI code change was needed** — the body is supplied
generically (`--json` / `--field k=v` / `key:=rawjson` / stdin), so the new
nested `owner` object flows through `--field owner:='{...}'` unchanged. Added a
`tenants create` example to the README so users don't hit `owner_required`
blind. The typed tree stays in lockstep with the contract by construction; the
only maintenance was the spec re-sync + the doc example.

## 2026-07-10 — Cover the device-login "approval-failed" poll branch

**Problem.** The device-login poll loop (`authLogin`, `cmd/realm-id/main.go`)
already distinguishes an approval-side failure (`approval_needs_app`,
`login_failed`, `approval_failed`, …) from a genuine `expired_token`/deadline
via its `switch` `default` branch — the fix that stops approval errors
masquerading as "expired" (BFF records the real reason on the device record;
the CLI surfaces it). But that `default` branch had **no test**, so a
regression that collapsed approval errors back into an "expired"/"timed out"
message would pass CI.

**Decision.** Add `TestAuthLogin_ApprovalFailed` (real message surfaced,
asserts the output is NOT "expired"/"timed out") and
`TestAuthLogin_ApprovalFailed_EmptyMessage` (the `approval failed (<code>)`
fallback), mirroring the existing `TestAuthLogin_AccessDenied` harness. No
production code changed — this closes the coverage gap only.

**Why not more.** The BFF + CLI production paths were already validated as
correct end-to-end (project validation pass, 2026-07-10); the only gap was the
missing regression guard.
