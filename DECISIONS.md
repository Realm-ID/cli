# Decisions — Realm-ID/cli

Rationale log for the `realm-id` CLI. WHAT-shipped lives in git/CHANGELOG; this
file records WHY. See the root `Realm-ID/project` DECISIONS.md for cross-cutting
context.


## 2026-08-25 (later+1) — the 9.7 MB binary is untracked, and the ignore pattern the TODO proposed is a trap

**Problem.** `cmd/realm-id/realm-id`, a compiled 9.7 MB binary, has been tracked
since `4e281ea` (2026-07-24). `.gitignore` covered `/realm-id` — anchored to the
repo root — so the `cmd/` copy was never ignored, and a `go build` run from that
directory dirties the tree with a multi-megabyte diff. Untracked with
`git rm --cached`; it is the only revision of the file, so nothing is rewritten.

**The TODO's proposed fix was wrong and that was MEASURED, not argued.** It said
to "widen the ignore to `realm-id` (unanchored)". An unanchored pattern matches
directories too, and `cmd/realm-id/` **is** a directory — so the pattern that
hides the binary also hides the package that produces it. Probed directly: with
`realm-id` unanchored, an untracked `cmd/realm-id/probe_new.go` is reported
ignored, naming that pattern. Already-tracked files keep tracking, which is
precisely why it would have looked fine on the day and broken on the next file
someone added — a silent `git add` no-op, the worst available failure for a
source file.

**Decision: spell both paths out** — `/realm-id` and `/cmd/realm-id/realm-id`.
Two lines instead of one, and neither can reach the source directory. The
alternative (`realm-id` plus a `!cmd/realm-id/` negation) re-earns the same
directory ambiguity for no gain. The reasoning is in the file as a comment,
because the next person to see two near-identical lines will want to collapse
them.

**Carry forward:** a `.gitignore` entry for a build artifact whose name matches
its package directory must be anchored. The build product and the source tree
share a name by convention in Go, so this is the normal case, not an edge one.

## 2026-08-25 (later) — the refusal was overturned the same day, and the reasoning against it is kept

**Decision (owner, 2026-08-25).** `POST /platforms/{id}/scopes/remove` ships as
`realm-id scopes remove`. The entry below refused it entry under ADR-062 §5; that
refusal stood for about an hour.

**The §5 case against exposing it was not wrong, and is preserved verbatim** —
in the entry below, in `skipDestructive`'s comment, and in the ADR amendment.
The operation is irreversible by its own spec text; under `on_empty=revoke` it
bulk-revokes keys this binary cannot re-mint, because ADR-097 §E filters the
mint; `?dry_run=true` is opt-in, which is the soft-gate shape §5 is explicitly
"stronger than"; and it selects its victims by discovery rather than by the
operator naming them. Every one of those still holds. **A filter that stops
naming what it gave up stops being reviewable**, which is why none of it was
deleted on the way to reversing the outcome.

**What the refusal overlooked.** Filtering the typed verb never removed the
capability — `realm-id api POST /platforms/<id>/scopes/remove --json …` has
always been reachable, and is the documented escape hatch for every §5
operation. So the filter did not stop the destructive act; it stopped the
*guard rails* around it, and left an operator hand-rolling the exact request
that decides whether live credentials get revoked. §5's premise is that absence
prevents the act. Here absence prevented only the safe path.

That is sharpest on the preview. `emptied` is a list of ROWS and the error
envelope is `{error, code}` with no payload, so the dry run is the ONLY surface
that can answer "which keys would this uncap?" — the question an operator must
answer before writing. A binary that could not reach it was, in practice,
pushing people toward the unguarded call.

**Recorded as an AMENDMENT to ADR-062 §5, not a bypass** (issuer `2855c0d`).
A §5 exception that lives only in a filter function is indistinguishable from a
§5 violation by anyone reading the ADR. The amendment is scoped to this one
operation and says so; `delete`, signing-key `rotate`, `suspend`/`unsuspend` and
ownership transfer stay absent.

**Implementation note that is easy to get wrong.** Dropping the path from
`skipDestructive` is not enough: `deriveCommand` treats a trailing static segment
as a collection noun unless `actionVerb` names it, so the op generates as the
bogus top-level `remove create`. `remove` joins `actionVerb`, and the guard pins
the GROUPING (`scopes` / `remove`), not merely the presence — mutation-verified:
removing it from `actionVerb` fails with the exact `remove create` shape.

**Guard.** `TestScopeRemoveIsExposedAsScopesRemove` replaces
`TestScopeRemoveIsFilteredButRenameSurvives` and is its inverse by design. It
keeps the sibling `scopes rename` as a positive control (its absence means the
spec failed to load and the test inspected nothing), asserts `--dry-run`
survives, and re-asserts that the other four destructive surfaces stay filtered.
Both mutations caught; suite 41 pass / 0 fail.

## 2026-08-25 — re-vendor the spec to 0.32.0; the one new operation is REFUSED entry, and the command tree does not move

Vendored `cmd/realm-id/openapi.yaml` from `0.30.0` to `issuer/docs/swagger.yaml`
at `0.32.0` (`go generate ./...`), and filtered the single operation it adds.

**The net effect on the binary is zero commands.** The before/after command-tree
dump is byte-identical once the filter is in. That is the finding, not a
disappointment: a `chore(spec)` commit that looks like "new capability" delivered
none, and the only thing the bump actually required was a policy decision.

### What the diff showed

One path added across the whole bump: `POST /platforms/{id}/scopes/remove`
(ADR-097 §G). Everything else in the 220-line spec diff is schemas and prose —
`RemoveScopeRequest` / `RemoveScopeResponse`, and the ADR-084 §7 amendment
rewriting what an empty `permissions_cap` means. **No existing operation changed
method, path, params or body**, which is the half of the diff that would have
been dangerous and is the half nobody thinks to check.

`scopes rename` was ALREADY present — it arrived with `0.30.0` (commit
`06b5f20`), not with this bump. Worth stating because the task that prompted this
work recorded both §F and §G as landing together.

### Why the removal is filtered rather than exposed

ADR-062 §5 states its rule as a property, not as its three named examples:
the verbs are absent so that *"a credential handed to an agent CANNOT perform an
irreversible action even if the agent tries"*, and it is explicitly *"stronger
than a `--yes` soft-gate"*.

The endpoint is that rule's subject on every count. Its own description says
**"Not reversible — neither the removal nor an accompanying revocation can be
undone"**: it deletes a scope string from every `permissions_cap` in the realm in
one transaction, and under `on_empty=revoke` it also revokes every key the
removal would uncap.

Three arguments for exposing it were considered and all fail:

- **"It ships a `?dry_run=true` preview."** The preview is OPT-IN, which is
  exactly the soft-gate shape §5 says it is stronger than. An agent omits it.
- **"It defaults to `on_empty=refuse`."** A default guards the *uncapping*
  hazard, not the irreversibility. `refuse` still permanently deletes the scope
  from every key that holds more than one.
- **"ADR-085 §8 already exempts key revocation."** That exemption's own
  justification is *"a replacement is one `create` away"* — and since ADR-097 §E
  this binary cannot mint a user API key at all (`skipBFFOnly`). A bulk revoke it
  cannot undo by re-minting is not the soft, re-mintable act §8 licensed, and it
  picks its victims by discovery rather than by the operator naming them.

`scopes rename` deliberately stays: a rename is undone by renaming back, so §5
does not reach it. The removal is not unreachable either — `realm-id api POST
/platforms/<id>/scopes/remove --json …` still works, the same escape hatch every
other §5 operation keeps. Revisit when machine-2FA/OTP step-up lands and §5
unlocks these behind `--otp`.

### The mis-derivation found on the way — reported, NOT fixed

Left unfiltered, the new operation generated as **`realm-id remove create`**: a
new top-level resource named `remove`, whose verb is `create`.

The first instinct was to add `"remove"` to `actionVerb`, which would have named
it `scopes remove`. **That was reverted after checking whether the case was
unique — it is not.** `deriveCommand` treats any trailing static segment as a
collection noun unless `actionVerb` names it, so every action segment absent from
that set already becomes a top-level resource with the verb `create`. Thirteen
commands are in that state today: you revoke a service account by running
`realm-id revoke create`, and import users with `realm-id import create`. The new
one would have been the fourteenth.

Fixing one of fourteen would have made the tree *less* consistent while reading
as a fix. Each needs its own naming decision, and renaming any of them is a
breaking change to a shipped binary — so the whole class is filed in `TODO.md`
instead. Recorded here because the correction is the useful part: the defect this
change first appeared to introduce was pre-existing and much wider.

### The guards

`TestScopeRemoveIsFilteredButRenameSurvives` asserts both halves — "remove is
absent" alone is satisfied by a tree that failed to load, so the sibling
`scopes rename` must be PRESENT for the absence to mean anything.

`TestTopLevelResourceGroupsAreReviewed` is the general one, and it is the answer
to the TODO item's actual complaint (*"nothing detects the drift"*). It pins the
set of resource groups the binary exposes and fails on any addition OR removal.

It is a **hand-maintained list, deliberately**, against this workspace's standing
rule that subject lists must be derived. The rule's target is silent decay: a
derived list answers *"is each thing we thought of still right?"*. This one
answers the question that actually went unasked — *"did the spec grow a command
nobody looked at?"* — and its only failure mode is going RED, which forces
exactly the review it exists to force. It catches removals too, which are worse
than additions: a partner's script breaks with no signal from a `chore` commit.
The thirteen mis-derived groups are pinned in it with a comment saying they are
recorded, not endorsed.

Both mutation-verified in both directions: dropping the `/scopes/remove` filter
turns both guards red (naming `remove create` in the failure message); also
filtering `/scopes/rename` turns the survives-half and the DISAPPEARED-half red.

### No version tag, and no CHANGELOG entry

**No tag was cut.** No command enters or leaves the binary, so there is nothing
for a consumer to consume; and the version is injected from the tag by
GoReleaser, which cannot run — GitHub Actions has been failing at startup
org-wide since 2026-08-16 (billing). Two commits already sit untagged past
`v0.2.11`, one of them breaking (`ea3b58d`, ADR-097 §E filtering
`user-api-keys create`); that backlog wants **`v0.3.0`** when releases resume,
and the decision belongs with whoever batches the release, not with this change.

**There is no `cli/CHANGELOG.md`.** This file's own header and `TODO.md` both
point at one and it has never existed — twelve tags with no release notes. Filed
in `TODO.md`; not backfilled here, because inventing twelve releases' worth of
notes from commit subjects would produce a document that looks authoritative and
is not.

## 2026-08-21 — `whoami` names the remedy; the countdown it asked for was both impossible and aimed at a fixed bug

`sessionHint` writes one line to STDERR when a BFF call fails with
`session_expired` / `session_missing` / `session_revoked`, naming the cause and
`realm-id auth login`. Stdout is untouched — ADR-062 makes it the
machine-readable channel.

### What the item asked for, and why none of it was built

`TODO.md` asked to *"decode the stored bearer's `exp` and print remaining
lifetime — CLI-only, small"*, after Traide hit a `401` ~38 minutes into
provisioning their prod realm (2026-06-29). Three separate things were wrong
with that.

**There is nothing to decode.** `cfg.SessionToken` is ADR-060's OPAQUE session
id plus a per-session AEAD key, not a JWT. It carries no claims.

**Nothing hands the CLI an expiry.** `POST /auth/device/token` returns exactly
`{session_token, realm_id, tenant_id, tenants}`. So the item was cross-repo, not
CLI-only.

**And the symptom no longer reproduces** — measured, not argued. Against a live
stack with `access_ttl_seconds` compressed to 1s, a BFF passthrough call made 5
seconds after login returned `200`: the passthrough self-heals an expired access
JWT and retries (`api/internal/middleware/passthrough.go:210`). A CLI session now
survives until its refresh (30d) or idle window ends, not until the access token
lapses.

**The measurement carried a positive control, and it was load-bearing**: a
silently-ignored config knob would have made the whole test vacuous — a 15-minute
token cannot expire during a 5-second sleep, so the `200` would have proved
nothing. Reading the config back was not enough either (the `v0.74.0`
`single_tenant_membership` defect stored a value that was never applied), so the
control asked the BFF's own `/token` for the resulting `expires_at`: **1 second**.

The most likely explanation for Traide's 38 minutes is that the self-heal was
**dead on arrival** — it matched an error code the issuer never sends on that
path — and was fixed on **2026-07-01**, two days after their report
(`api/DECISIONS.md:857`). Stated as the likely explanation, not a finding: the
numbers do not line up exactly (15m ≠ 38m) and nobody re-tested at the time.

### What was actually built, and why it is smaller than the ask

The BFF already emits a coded `session_expired`, and the CLI already prints the
body — so the CAUSE was on screen all along and the item's "no obvious cause" was
overstated. What was missing is the REMEDY. One stderr line, using the
`errorCode` helper that already existed.

Only the three session-lifecycle codes qualify. Hinting "log in again" after a
PERMISSION failure would send the operator round a loop that cannot help,
relabelling an authorization problem as an authentication one.

### Mutation testing found a gap in the tests, not in the code

Four mutations. Moving the hint to stdout failed immediately. **Dropping the
status guard and dropping the code switch both left the suite GREEN.**

The cause is worth keeping: every case varied status and code *together*
(401+session code, 403+permission code), so the suite pinned the CONJUNCTION by
accident and neither half on its own. **A test set that only ever moves two
variables in lockstep cannot tell which one the code reads.**

Two cases close it — a `401` with a non-session code, and a session envelope
under a non-401 status. The second needed a realistic fixture, and this codebase
supplies one: the documented **GoFr typed-nil→206 trap**, where a handler
returning a helper's `(typedNilData, err)` pair collapses a 4xx into a `206`
carrying a real error envelope. So a genuine `session_expired` body really can
arrive under a success-class status — and hinting there would tell the operator
their session is dead at the moment it demonstrably is not. All four mutations
now fail.

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
