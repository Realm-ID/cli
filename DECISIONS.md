# Decisions — Realm-ID/cli

Rationale log for the `realm-id` CLI. WHAT-shipped lives in git/CHANGELOG; this
file records WHY. See the root `Realm-ID/project` DECISIONS.md for cross-cutting
context.


## Index

15 entries. Newest first.

- [2026-09-05 — re-vendor to 0.46.0: the ADR-101 seat guard reaches `role-templates update`](#2026-09-05--re-vendor-to-0460-the-adr-101-seat-guard-reaches-role-templates-update)
- [2026-09-04 — re-vendor to 0.44.0: a six-release catch-up that moved nothing in the command tree](#2026-09-04--re-vendor-to-0440-a-six-release-catch-up-that-moved-nothing-in-the-command-tree)
- [2026-08-30 — re-vendor to 0.37.0: a new resource group, exposed on purpose](#2026-08-30--re-vendor-to-0370-a-new-resource-group-exposed-on-purpose)
- [2026-08-30 — re-vendor to 0.36.0: the ADR predicted a resource would disappear, and the binary says otherwise](#2026-08-30--re-vendor-to-0360-the-adr-predicted-a-resource-would-disappear-and-the-binary-says-otherwise)
- [2026-08-28 — thirteen action segments were top-level "resources" with a `create` verb; the derivation now refuses to guess](#2026-08-28--thirteen-action-segments-were-top-level-resources-with-a-create-verb-the-derivation-now-refuses-to-guess)
- [2026-08-27 — re-vendor to 0.33.0: the command tree moves in BOTH directions, and the §5 amendment loses its subject](#2026-08-27--re-vendor-to-0330-the-command-tree-moves-in-both-directions-and-the-5-amendment-loses-its-subject)
- [2026-08-25 (later+1) — the 9.7 MB binary is untracked, and the ignore pattern the TODO proposed is a trap](#2026-08-25-later1--the-97-mb-binary-is-untracked-and-the-ignore-pattern-the-todo-proposed-is-a-trap)
- [2026-08-25 (later) — the refusal was overturned the same day, and the reasoning against it is kept](#2026-08-25-later--the-refusal-was-overturned-the-same-day-and-the-reasoning-against-it-is-kept)
- [2026-08-25 — re-vendor the spec to 0.32.0; the one new operation is REFUSED entry, and the command tree does not move](#2026-08-25--re-vendor-the-spec-to-0320-the-one-new-operation-is-refused-entry-and-the-command-tree-does-not-move)
- [2026-08-21 — `whoami` names the remedy; the countdown it asked for was both impossible and aimed at a fixed bug](#2026-08-21--whoami-names-the-remedy-the-countdown-it-asked-for-was-both-impossible-and-aimed-at-a-fixed-bug)
- [2026-08-06 — re-vendor the spec to 0.24.0; `platforms describe` costs a `cp`](#2026-08-06--re-vendor-the-spec-to-0240-platforms-describe-costs-a-cp)
- [2026-08-06 — `platforms set-config` bound to GET or PATCH at random, per run](#2026-08-06--platforms-set-config-bound-to-get-or-patch-at-random-per-run)
- [2026-08-05 — service mode never worked, and the test was holding it that way](#2026-08-05--service-mode-never-worked-and-the-test-was-holding-it-that-way)
- [2026-07-24 — Re-sync vendored spec for owner-required tenant create (ADR-073 Amendment C)](#2026-07-24--re-sync-vendored-spec-for-owner-required-tenant-create-adr-073-amendment-c)
- [2026-07-10 — Cover the device-login "approval-failed" poll branch](#2026-07-10--cover-the-device-login-approval-failed-poll-branch)

## 2026-09-05 — re-vendor to 0.46.0: the ADR-101 seat guard reaches `role-templates update`

**Problem.** The vendored spec (`0.44.0`) was two issuer releases behind
(`0.45.0`/issuer `v0.121.0` then `0.46.0`/issuer `v0.121.1`). `0.45.0` added
the ADR-101 seat guard to `PATCH`/`DELETE /platforms/{id}/role-templates/{templateId}`
— an `override_seated` query param plus `409 role_template_seated` and
`503 role_template_seat_check_failed`; `0.46.0` only corrected what counts as
a seat (federation bindings now count) and reworded the `409` description, no
new paths or parameters.

**Decision.** Re-vendor from `issuer/docs/swagger.yaml`, then diff the command
tree via the CLI's own `buildCommands()` (before/after, the same
`zzdump_test.go` scratch harness as the 0.44.0 entry above, never committed).

**Result: exactly one flag reaches the binary.** `role-templates update`
gains `--override_seated` (an optional query flag on the existing PATCH
command); 0 commands added/removed, 0 required-ness changes elsewhere.
`role-templates` stays create/list/update with **no `delete` verb** —
confirmed unaffected: `skipDestructive` filters every DELETE that is not an
ADR-085 §8 revocation BEFORE `buildCommands` ever classifies it, so the spec
carrying a `delete` operation on this path (it always has) was never the
reason the CLI lacked one, and re-vendoring changes nothing about that filter.
The new `409 role_template_seated` / `503 role_template_seat_check_failed`
responses are response-shape/status additions the command struct doesn't
model and needed no code change.

**No compat shim needed** — pure drop-in. `go build ./...`, `go vet ./...`
clean; `go test ./...` green, no skips introduced.

## 2026-09-04 — re-vendor to 0.44.0: a six-release catch-up that moved nothing in the command tree

**Problem.** The vendored spec was tracked as "FIVE issuer releases behind" —
that title itself was stale; the measured gap was **SIX** (`0.38.0` → `0.44.0`).

**Decision.** Re-vendor from `issuer/docs/swagger.yaml`, then diff the command
tree via the CLI's own `buildCommands()` (before/after, `zzdump_test.go`
scratch harness, never committed), per this repo's own convention of diffing
rather than inferring from a path count.

**Result: the tree does not move.** 0 commands added/removed, 0 flags renamed,
0 required-ness changes. 3 endpoints gained OPTIONAL pagination flags only
(`--cursor`/`--limit` on `service-accounts list`, `sources list`, `sso-domains
list`), tracking the issuer's pagination-input-validation rollout across 23
operations. `role-templates` (create/list/update, no delete) is unchanged,
still no `delete` verb per ADR-062 §5. The new `409 last_owner` and
`400 invalid_cursor`/`400 invalid_limit` responses are response-shape/status
additions the command struct doesn't model (method/path/params/query/body-
presence only) and needed no code change — the CLI already forwards whatever
status the issuer returns.

**No compat shim needed** — pure drop-in. `go build ./...` clean; `go test
./...` green, 12.172s, no skips introduced.

## 2026-08-30 — re-vendor to 0.37.0: a new resource group, exposed on purpose

**What.** Spec re-vendored to issuer `0.37.0`. It carries ADR-101 D1's write
side, so a NEW top-level resource group — `role-templates` — reached the binary:
`create`, `list`, `update`.

**Why it is exposed.** `TestTopLevelResourceGroupsAreReviewed` fails on any new
group until a human decides, which is the whole point of that guard: a spec
re-vendor must not be able to grow the CLI's surface silently. The decision here
is to expose it, on the same reasoning that keeps `roles` after ADR-101 — the
surface is base-realm-GATED, not RI-private. A partner who runs it gets an
honest `403 role_authoring_retired` naming the ADR-097 replacement, which is a
better outcome than a resource that simply is not there when RealmID's own
operators need it. And D1 exists precisely so RealmID can add a role WITHOUT a
release; an agent-first CLI is the natural place to script that.

**Why there is no `delete`.** ADR-062 §5 skips a destructive DELETE that is not
a revocation, so removing a template is not offered — exactly as `roles` offers
no `delete`. Worth stating because its absence looks like a derivation gap and
is not: the guard surfaced the group for review, and this is the review.

## 2026-08-30 — re-vendor to 0.36.0: the ADR predicted a resource would disappear, and the binary says otherwise

**What happened.** ADR-101's consequences list states: "**The `roles` resource
disappears from the CLI** via the spec re-vendor, since CLI commands derive from
the embedded swagger at runtime." The re-vendor happened. The resource did not
disappear.

**Why the prediction was wrong.** ADR-101 D4 gates authoring on the base realm;
it does not delete the routes. `POST /platforms/{id}/roles` and its three
siblings still exist and now answer `403 role_authoring_retired` outside
RealmID's own realm, while `GET /roles` and `disable`/`enable` are untouched and
open to every realm owner. Only `POST /platforms/{id}/starter-roles` was
actually deleted, so only `starter-roles create` left the tree.

**Decision — the CLI keeps them.** A tree that hid four commands the API still
serves would be lying about the contract, and this CLI's whole design is that
the tree IS the contract: it derives at runtime precisely so it cannot drift
from what the server offers. A user who runs `roles create` against a partner
realm should get the server's own `403` with its own explanation, which names
the replacement (product roles reach RealmID as ADR-097 scopes), rather than
"unknown command".

**The guard is what surfaced this, and it did so as a REFUSAL.**
`TestTopLevelResourceGroupsAreReviewed` failed with "resource group
`starter-roles` DISAPPEARED from the binary. A removal breaks callers silently;
confirm it was intended, then drop it from this list." That is the check working
exactly as designed — a re-vendor is a bulk import of someone else's decisions,
and the one failure mode worth blocking on is a capability vanishing without
anybody noticing. Confirming the removal is a two-line edit; NOT being told would
have been a silent break.

**Recorded in the list rather than only here**, with the `roles` non-removal
next to it, because the next person reading ADR-101 will look for the resource
to be gone and needs to find out why it is not.

## 2026-08-28 — thirteen action segments were top-level "resources" with a `create` verb; the derivation now refuses to guess

**One pass, all of them, no release.** `TODO.md` recorded that fixing one of
these was tried and reverted, because a single rename makes the tree *less*
consistent while reading as a fix. That judgement stands, and it is why this is
one change covering every case rather than fourteen small ones. It also ships
`CHANGELOG.md`, whose first entry is this break.

### RCA

**Symptom.** `realm-id revoke create` revoked a service account.
`realm-id import create` imported users. `realm-id tenant-choice create` settled
a login picker. Thirteen top-level "resources" in `realm-id --help` that name no
resource, each with the verb `create`, none of which creates anything. Three
further operations — `roles disable`, `roles enable`, `sessions revoke` — were
not merely misnamed but **absent**, and nothing said so.

**Root cause.** `deriveCommand`'s three rules were not total. A trailing static
path segment was an action if `actionVerb` named it and a **collection noun
otherwise** — the default was to assume the segment was a resource. So the
correctness of the whole tree rested on a hand-maintained allowlist being
complete, and it never was: `actionVerb` grew one segment at a time, each added
because someone happened to be looking at that path (`config` in the 2026-08-06
random-binding fix, `remove` in the 2026-08-25 amendment). Every segment nobody
had happened to look at silently took the other branch.

The *second-order* cause is worse than the naming. Two mis-derived segments
(`disable`, `enable`) appear on two different parents each, and `revoke` on two;
because the bogus group name came from the segment rather than the parent, those
pairs **collided**, and `buildCommands`' "fewest path params wins" tie-break
resolved each collision by discarding one operation. A missing allowlist entry
did not just produce a bad name, it deleted a capability.

**Why it wasn't caught.** `TestTopLevelResourceGroupsAreReviewed` *did* see all
thirteen — and pinned them, with a comment marking them "not endorsed". The
guard was working; what was missing was a rule it could fail against. It asked
"did the group set change?", which the mis-derivation answers "no" forever. No
test asked "is every segment in the spec something we have classified?", so the
unclassified state was not nameable and therefore not assertable. The three
dropped operations had no guard at all: `buildCommands` returns `dropped` and
nothing inspects it.

**Fix.** The classification is now total and the default inverted:

- `actionVerb` gains the thirteen segments. Twelve keep their own name as the
  verb (`service-accounts deactivate`); `pending` becomes `list-pending`,
  matching `mine` → `list-mine`, because `GET /domains/pending` is a filtered
  list and `domains pending` would read as an imperative.
- Collection nouns are now **derived from the spec**, not listed: a segment that
  some path follows with a further segment is being used as a collection there.
  That covers most of them and leaves a small hand-written
  `listOnlyCollections` for the eight collections with no item route
  (`audit-events`, `permissions`, `stats`, …), which the structural rule cannot
  see.
- A trailing segment in neither set is **unclassified**, and `deriveCommand`
  returns no command for it. Absent is recoverable (`realm-id api`); a
  confidently wrong command is not, and that is exactly the failure being fixed.

`me tenant-choice` is the one name that needed a judgement call: `/me/tenant-choice`
has no intermediate collection, so the parent segment is `me`. Taking the
derived answer keeps the rule uniform. The alternative — treating `/me/` as a
group prefix the way `/admin/` is treated — would have renamed
`invitations accept`/`reject`, which work today.

**Prevention.** `TestEverySpecSegmentIsClassified` walks the embedded spec,
derives the subject list from it, and fails while any trailing segment is
unclassified — so the next re-vendor that introduces one goes red and forces the
naming decision that did not happen this time. Its subject list is derived, not
hand-written, which is the property the old guard lacked; a positive control
(≥30 segments found) stops it passing on a spec that failed to load, since
"empty" is its pass condition. `TestUnclassifiedTrailingSegmentIsSkippedNotInvented`
covers the skip branch on a synthetic path — it is unreachable from the real
spec by construction, and without it the guard clause could be deleted with the
suite still green (verified: that mutation is caught only by this test).
`TestActionSegmentsDeriveOnTheirParentResource` pins all sixteen commands
positively **and** the thirteen dead group names negatively, so a regression
reports a diagnosis rather than only "a group disappeared".

### Why the two lists are still partly hand-maintained

This workspace's standing rule is to derive a check's subject list rather than
write it. `listOnlyCollections` is written by hand, and the reason is that the
alternative is worse: the structural signal for "collection" is an item route,
and eight real collections in this spec have none. A derived-only rule would
classify `audit-events` as an action and break `audit-events list`, which works.
What makes the hand-written half acceptable is that it can now only decay by
going **RED** — an unclassified segment is a test failure, not a silently
invented command. That is the same argument that justified
`TestTopLevelResourceGroupsAreReviewed` being hand-maintained, and it is the
part that was missing before: that guard could rot silently, because the
mis-derivation it was pinning looked exactly like a decision.

### Not released, deliberately

Fourteen tags exist (`v0.2.0`–`v0.3.1`; the TODO item that asked for the
changelog said twelve, written before `v0.3.0`). Every rename here breaks a
script written against any of them. Tagging is a separate act with its own
decision — there is nothing else queued behind this, and a break should not ride
out as a side effect of the commit that caused it. The break is written up in
`CHANGELOG.md` under *Unreleased* with an old → new table.

## 2026-08-27 — re-vendor to 0.33.0: the command tree moves in BOTH directions, and the §5 amendment loses its subject

**What.** `cmd/realm-id/openapi.yaml` re-vendored from issuer spec `0.32.0` to
`0.33.0` (ADR-100). This is the first re-vendor where the generated tree moves in
both directions at once, and both moves needed a decision rather than a diff.

**`scopes remove` disappears, and the 2026-08-25 amendment goes with it.** Two
days ago §5 was amended by explicit owner decision to expose a bulk, irreversible
operation the rule read on its own terms would have filtered — the reasoning
against it was kept verbatim in `spec.go` precisely because it was the cost of
the decision. ADR-100 D10 has now deleted the endpoint outright, everywhere, so
there is no operation left for the amendment to cover. **Superseded, not
reversed**: the decision was correct for the design as it stood. §5 is back to
having no exceptions beyond revocation, and the long justification is retired
from `spec.go` to `git log` — a filter that documents an endpoint nobody can call
stops being readable.

`TestScopeRemoveIsExposedAsScopesRemove` inverted into
`TestScopeRemoveIsGoneAndRenameSurvives`. It keeps `scopes rename` as the
positive control for the same reason the old test did: an absence assertion is
satisfied by a tree that failed to load.

**`user-api-keys update` appears, and must not.** ADR-100 D12's
`PUT …/user-api-keys/{id}` carries the SAME ADR-097 §E escort as the mint, and
this binary holds a user token from the device flow — so the verb would generate
cleanly, appear in `--help`, and 401 at runtime, which is worse than not
existing because the operator cannot tell a missing capability from a broken one.

**The filter is METHOD-aware, and that is the whole decision.** `skipBFFOnly`
was `POST && HasSuffix("/user-api-keys")`; the obvious widening — match the path
`/user-api-keys/` — would ALSO catch `DELETE …/user-api-keys/{id}`, the revoke,
which is the same path shape. ADR-084 §9 and the partner guide both name
revocation as the primary control for an end-user key, so a binary that could not
revoke one would contradict the documentation shipped with it. The new test
asserts both directions, and mutation-checking it (reverting the filter to the
old one-line form) fails it by name.

**Nothing else in the tree moved.** `user-api-keys list` / `revoke` and
`scopes rename` are unchanged, and the top-level resource list is byte-identical.

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
