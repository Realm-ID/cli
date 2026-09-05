# TODO — cli (`realm-id`)

Open work only; shipped items live in `CHANGELOG.md` / git tags.

> **Re-verified 2026-08-03** against the tree — all four items still stand, with
> the cited line numbers refreshed: `resolveCredential`
> (`cmd/realm-id/commands.go:312-317`) still returns the raw `rk_live_…` as the
> bearer for the issuer, and `authWhoami` (`cmd/realm-id/main.go:501-512`) still
> prints `/me` verbatim with no `exp` decode.

---

## The vendored spec is FIVE issuer releases behind (`0.38.0` vs `0.44.0`)

- [x] **CLOSED 2026-09-04.** Re-vendored `cmd/realm-id/openapi.yaml` from
      `issuer/docs/swagger.yaml`, `info.version` `0.38.0` → `0.44.0`. The
      title's "FIVE" was itself stale — the measured gap was **SIX**. Command-
      tree diff (via the CLI's own `buildCommands()`, before/after) is the
      deliverable: **0 commands added/removed, 0 flags renamed, 0
      required-ness changes.** 3 endpoints gained optional pagination flags
      only (`--cursor`/`--limit` on `service-accounts list`, `sources list`,
      `sso-domains list`), matching the issuer's pagination-input-validation
      rollout. `role-templates` (create/list/update, no delete) unchanged,
      still no `delete` verb, per ADR-062 §5. The `409 last_owner` and the new
      `400 invalid_cursor`/`400 invalid_limit` are response-shape/status
      additions the command struct doesn't model and needed no code change —
      the CLI already forwards whatever status the issuer returns. No breaking
      changes; `go build ./...` clean, `go test ./...` green (12.172s).
      ~~Original finding:~~ Measured 2026-09-04. `cmd/realm-id/openapi.yaml` declares `info.version 0.38.0`;
the issuer serves `0.44.0`. The path count matches (120 both sides), so the
command TREE is probably unaffected — but that is an inference from one number,
not a diff, and it is exactly the inference this repo's own convention warns
against: the CLI derives its surface from the embedded spec at runtime, so a
spec re-vendor is how a new verb arrives, and a stale spec is how one silently
does not.

What changed across those five versions is not audited here. At minimum
`0.44.0` documents the `409 last_owner` on
`PATCH /tenants/{id}/users/{uid}/status`, which the CLI currently describes as
`200`-only.

Not folded into the ADR-107 release that surfaced it: a five-version catch-up
needs its own command-tree diff (`realm-id --help` before/after, per the repo
convention), and smuggling it into an unrelated release is how an unreviewed
surface change ships.

**Do this as: re-vendor, diff the command tree, then tag.** In that order.


## Device-flow DX (Traide integration feedback, 2026-06-29)

Surfaced provisioning the Traide prod realm via CLI device login
(`../tally-helper/docs/realmid-integration-process-feedback-2026-06-29.md` §1).
The docs side is handled (`README.md`: re-auth, `REALM_ID_API_KEY` for long runs,
warning against concurrent `auth login`); these are the code fixes.

> ~~**Surface token expiry in `realm-id auth whoami`**~~ — **RESOLVED
> 2026-08-21, but NOT as asked.** `sessionHint` names the cause and
> `realm-id auth login` on stderr for the three session-lifecycle codes; stdout
> stays pure JSON.
> **The countdown was not built, for three reasons, and the third was
> MEASURED.** (1) The bearer is ADR-060's opaque id, not a JWT — nothing to
> decode. (2) `POST /auth/device/token` hands the CLI no expiry, so it was
> cross-repo, not CLI-only. (3) **The symptom no longer reproduces**: against a
> live stack with `access_ttl_seconds` compressed to 1s (positive control: the
> BFF's `/token` reported a 1-second `expires_at`, because a stored-but-unapplied
> knob would have made the test vacuous), a passthrough call 5s after login
> returned `200` — the BFF self-heals an expired access JWT. Traide's ~38 minutes
> was most likely the self-heal being dead on arrival, fixed 2026-07-01, two days
> after their report. Likely explanation, not a finding: 15m ≠ 38m.
> Four mutations; **two initially survived and the gap was in the TESTS** — every
> case moved status and code together, so the suite could not tell which the code
> read. Rationale: `DECISIONS.md` 2026-08-21. Original entry:
>
> **Carried lesson from the approve-side-error item** (record purged 2026-08-09
> per this file's "open work only" rule; full account in `Realm-ID/project`'s
> `DECISIONS.md` 2026-08-06). It had been DONE for five weeks and three TODO
> files said otherwise, because **every layer was tested only against a
> hand-written stub of the next one** — `TestAuthLogin_ApprovalFailed` writes the
> BFF's JSON envelope *inside the test*, so it passes whether or not the real BFF
> emits that shape. Nothing observed the seam, so the honest reading of the tree
> was the pessimistic one. The 201-vs-200 bug lived in that same seam and was
> found in production. When this CLI's behaviour depends on another repo's wire
> shape, the test that proves it must put a real server on the other end.
- [ ] **Distinguish "this code was already consumed by another session" from
  "unknown or expired"** on the `/device` approval page.
  ⚠️ **RE-SCOPED 2026-08-24 — the original premise is STALE.** It read "the
  approval page doesn't show *which* run/code it's authorizing"; it does, and
  has: `ui/web/src/DeviceApprove.tsx:227-230` renders the code under
  `data-testid="device-user-code"`, and `:68` already surfaces "This
  authorization code is unknown or has expired. Start the login again in your
  terminal." What remains is only the one distinction above — a consumed code
  and an unknown one produce the same sentence, and the two call for different
  operator actions. Original entry, kept for its context:
> ~~**Bind the `/device` approval page to a specific `device_code`**~~
> (cross-repo: issuer + `ui/web`, not CLI-only). The approval page doesn't show
> *which* run/code it's authorizing, so running `auth login` in two terminals and
> approving one while watching the other's poller looks like an indefinite hang
> (`authorization_pending` forever) — Traide filed false "STILL-BROKEN" reports
> over exactly this self-inflicted race. Fix: have the page display/confirm the
> `device_code` (or `user_code`) being approved, and/or surface "this code was
> already consumed by another session" instead of silent pending. Touches the
> issuer `/auth/device/approve` surface + `ui/web/src/main.tsx` `/device` branch.
> *(Partially mitigated: `cli/v0.2.7` added a hard OS-lockfile singleton for
> `auth login`, so the CLI itself can no longer produce two live codes on one
> machine. The multi-machine / stale-tab case remains.)*

## ~~CI runs NO tests at all~~ — **RESOLVED 2026-09-05**

Until 2026-09-05, `release.yml` ran `changelog-hygiene.sh` and `goreleaser
release`, and nothing in `.github/` or `.goreleaser.yaml` invoked `go test`.
**This repo's test suite had never gated anything.**

It stopped being theoretical on 2026-09-05: the `queryParamLabel` regression
test added that day (`cmd/realm-id/commands_test.go`) exists specifically so a
future refactor cannot silently relabel a write-side flag as a read filter —
and it would never have run. A test that no runner executes is not a gate; it
is a comment that takes longer to write.

Note also that "run it the way CI runs it" **was** a NULL instruction on this
repo and silently produced a false green for anyone who followed it. That
phrasing is correct guidance everywhere else in the workspace, which is exactly
what made it dangerous here. It is now true here as well.

**Fixed 2026-09-05** by `.github/workflows/tests.yml`: gofmt, `go build`,
`go vet`, `go test -race`, on push-to-main + pull_request + workflow_dispatch.
Go `1.23`, matching `release.yml` and `go.mod` rather than the issuer's `1.26` —
the gate must compile what goreleaser actually ships.

Two choices worth keeping:

* **The release is gated too, not just the branch.** `tests.yml` also declares
  `workflow_call`, and `release.yml`'s `goreleaser` job now `needs: [changelog,
  test]`, invoking that same workflow against the TAGGED tree. Without it the
  `paths-ignore: '**.md'` filter would reproduce the issuer's hole — that repo
  tags a docs-only CHANGELOG commit, so the tagged SHA got no run and two
  releases were promoted on a guessed verdict. Invoking the workflow rather
  than copying its steps means a release can never be gated by a weaker check
  than a routine commit is.
* **`-race` and `gofmt` were measured clean BEFORE being added**, not assumed:
  0 races over the one package in 21.2s, and `gofmt -l cmd` empty. Ratcheting
  either one in while it is red is an open-ended debugging session; while it is
  green it is one line.

Note the concurrency group carries a literal `tests-` prefix: under
`workflow_call`, `github.workflow` resolves to the CALLER's name, so without it
the group would collide with `release.yml`'s own — which sets
`cancel-in-progress: false` on purpose where this one sets `true`.

## Broken today

> ⚠️ **THIS SECTION HOLDS ONE OPEN ITEM — read to the bottom.** It is below the
> FIXED-notes, not above them: the vendored-spec version check. The mis-derived
> `create` commands and the missing `CHANGELOG.md` were CLOSED 2026-08-28 and
> are recorded at the bottom as struck records.
>
> **This banner said "THIS SECTION IS EMPTY" until 2026-08-28**, which stopped
> being true on 2026-08-25 when those three were filed under it. It was written
> on 2026-08-21 for the opposite failure — the heading alone reading as a live
> pool of bugs when everything under it was fixed — and then survived the
> additions unchanged. **A banner that tells a sweep to skip a section is a
> claim with a date on it, and this one outlived its truth by three days**; it
> is the same shape as the root `TODO.md` items stranded under a RETIRED
> heading (umbrella `DECISIONS.md` 2026-08-28, TODO sweep). Keep it accurate or
> delete it — a stale "nothing here" is worse than no banner, because it is
> believed.

> **FIXED 2026-08-06 — `platforms set-config` bound to GET or PATCH at random.**
> `actionVerb` keyed on the trailing path segment alone, so the GET and the
> PATCH on `/platforms/{id}/config` derived the same `(group, verb)`. Their path
> params are equal in number, so `buildCommands`' "fewest params wins" tie-break
> could not separate them either and kept whichever came first out of a
> RANDOMIZED Go map iteration. Same binary, same spec, different answer per run;
> a run that bound GET would accept the operator's values and issue a read.
> Now method-aware (`get-config` / `set-config`), which renames nothing else —
> `config` is the only action segment in the spec with more than one non-DELETE
> method. Guarded by `TestBuildCommandsIsDeterministic`, which rebuilds the tree
> 51× and derives its subject list from the SPEC rather than from a hand-written
> list of known collisions. RCA in `DECISIONS.md` 2026-08-06.
>
> **The near-miss worth keeping:** this was found while diffing the command tree
> before re-vendoring the spec — not by a test, and not by anyone using the
> command. A 50/50 binding presents to a user as "it worked yesterday".

> **FIXED 2026-08-05 — "Service mode does not work against the issuer."**
> `resolveCredential` now performs the ADR-051 exchange
> (`POST /auth/login {grant_type: platform_api_key}`) and bears the returned
> platform JWT; the raw `rk_live_...` is never sent as a bearer. Cached per
> PROCESS, not persisted — ADR-089 returns no refresh token on this lane, so the
> key is the renewable credential and a JWT on disk would be a second bearer at
> rest for no gain. The README's "it uses that platform key" line is corrected in
> the same change.
>
> **The finding worth keeping: the old test was PROTECTING the bug.**
> `TestResolveCredential` asserted `bearer == "rk_live_1"` — it restated the
> implementation, so it passed for exactly as long as service mode was broken and
> would have failed the moment anyone fixed it. Nothing had ever put a server on
> the other end of service mode. The replacement asserts the CONTRACT against an
> httptest issuer (exchange path, grant type, no Authorization on the bootstrap
> call, bearer is the JWT and explicitly NOT the raw key, no re-exchange) and is
> mutation-verified. RCA in `DECISIONS.md` 2026-08-05.
>
> This does NOT close "the CLI can act as the BFF" (root `TODO.md`): presenting
> the ADR-088 escort bearer plus a forwarded `X-User-Token` for on-behalf calls
> is still unbuilt. Service mode now works for platform-authority commands.

*(No open chores. The `gofmt -l` violation on `cmd/realm-id/main.go` was verified
clean 2026-07-28 and the item was removed; this line goes at the next sweep.)*

> ~~**The vendored `cmd/realm-id/openapi.yaml` is two spec versions stale**~~ —
> **CLOSED 2026-08-25.** Re-vendored `0.30.0` → `0.32.0` via `go generate ./...`.
> **The command tree did not move: the before/after dump is byte-identical.**
>
> **Two of the item's own premises were STALE when it was closed.** It says
> "declares `0.24.0` while the issuer is at `0.26.0`" — measured 2026-08-24, but
> commits `fc2ce2d`/`ea3b58d`/`06b5f20` had already carried the vendored copy to
> `0.30.0`. The drift claim survived (two versions), the numbers did not.
> Second: `scopes rename` was already generating, from `0.30.0` (`06b5f20`) —
> §F and §G did not land together.
>
> **The whole `0.30.0` → `0.32.0` bump adds ONE path**,
> `POST /platforms/{id}/scopes/remove` (ADR-097 §G). The other ~200 diff lines
> are schemas and prose (`RemoveScopeRequest`/`Response`, the ADR-084 §7
> amendment on an empty `permissions_cap`). **No existing operation changed
> method, path, params or body** — the half of the diff that would have been
> dangerous, and the half nobody thinks to check.
>
> ~~**The new operation is FILTERED, not exposed** (ADR-062 §5)~~ —
> **OVERTURNED THE SAME DAY by owner decision (2026-08-25). It ships as
> `realm-id scopes remove`.** The §5 reading above is correct and is kept
> verbatim, because it is the COST of the decision, not a mistake: the op is
> irreversible by its own description, under `on_empty=revoke` bulk-revokes keys
> this binary cannot re-mint (ADR-097 §E filters the mint), and `?dry_run=true`
> is opt-in — the soft-gate shape §5 is explicitly "stronger than".
> **What the filter overlooked:** it never removed the capability, only the safe
> path to it. `realm-id api POST …/scopes/remove --json …` was always reachable,
> so filtering the typed verb left an operator hand-rolling the request that
> decides whether live credentials get revoked — and the dry-run preview is the
> ONLY surface that can report which keys a removal would uncap (`emptied` is a
> row list; the 409 envelope carries no payload).
> Recorded as an **amendment to ADR-062 §5**, not a bypass:
> `issuer/docs/adr/062-agent-cli-and-device-flow-auth.md` § *Amendment
> (2026-08-25)* (issuer `2855c0d`). `remove` joins `actionVerb` so the op derives
> as `scopes remove` rather than the bogus top-level `remove create` it defaults
> to. `TestScopeRemoveIsExposedAsScopesRemove` pins the exposure, the GROUPING,
> the `--dry-run` flag, and re-asserts that delete / signing-key rotate /
> suspend / owner-transfer stay filtered, so a widening must be deliberate.
> Both mutations verified. Full reasoning in `DECISIONS.md` 2026-08-25.
>
> **"Nothing detects the drift" is now false.**
> `TestTopLevelResourceGroupsAreReviewed` pins the set of resource groups the
> binary exposes and fails on any addition OR removal, so a future re-vendor
> that grows a command cannot land unreviewed. It is deliberately a
> hand-maintained list: its only failure mode is going RED, which forces the
> review. Mutation-verified in both directions, as is
> `TestScopeRemoveIsFilteredButRenameSurvives`.
>
> **The version-comparison check the item asked for was NOT built**, and the
> reason is worth keeping — see the open item below.

- [ ] **No test compares the vendored `info.version` against the issuer's** —
      `cmd/realm-id/openapi.yaml` vs `issuer/docs/swagger.yaml`. Asked for by the
      re-vendor item closed above and deliberately not built there: the check is
      **cross-repo**, and `Realm-ID/cli`'s CI checks out only this repo, so a test
      reading `../../../issuer/docs/swagger.yaml` would `t.Skip` in the one place
      it needs to run — a guard that reports nothing. It belongs in the umbrella
      repo's cross-repo CI (or needs the ADR-062 §6-era deploy-key setup), not in
      `cmd/realm-id/spec_test.go`. Until then the drift is caught by a human
      diffing the tree, plus `TestTopLevelResourceGroupsAreReviewed` catching the
      subset of drift that changes the command surface.

> ~~**Fourteen commands are mis-derived as top-level "resources" whose verb is
> `create`**~~ — **CLOSED 2026-08-28**, in the one deliberate pass this item
> asked for. All of them derive as `<parent collection> <action>` now:
> `revoke create` → `service-accounts revoke`, `import create` → `users import`,
> and so on for `deactivate`, `delink`, `disable`, `enable`, `hand-back`,
> `leave`, `request`, `reset-handle`, `revoke-all`, `tenant-choice`, plus
> `pending list` → `domains list-pending` (a filtered list, named like
> `list-mine`). Old → new in full: `CHANGELOG.md` *Unreleased*; reasoning + RCA
> in `DECISIONS.md` 2026-08-28.
>
> **The count in the heading was wrong in two directions, and the correction is
> the finding.** Thirteen bogus GROUPS shipped, not fourteen — but sixteen spec
> operations were affected, because `disable`, `enable` and `revoke` each appear
> under two parents. Named after the segment instead of the parent, those pairs
> COLLIDED, and `buildCommands`' "fewest path params wins" tie-break resolved
> each by discarding one operation. `roles disable`, `roles enable` and
> `sessions revoke` were not misnamed, they were **ABSENT**, and no test looked
> at `dropped`. A missing allowlist entry deleted a capability, it did not just
> mis-spell one. The tree goes 94 commands / 44 groups → 97 / 35.
>
> **What actually changed is the default, not the list.** `actionVerb` gained
> the thirteen segments, but the durable half is that an unclassified trailing
> segment now yields NO command instead of a guessed one, and
> `TestEverySpecSegmentIsClassified` — subject list derived from the embedded
> spec, with a positive control because "empty" is its pass condition — goes red
> while any segment is unclassified. `TestTopLevelResourceGroupsAreReviewed` had
> been *pinning* all thirteen with a "not endorsed" comment since 2026-08-25:
> the guard worked, there was simply no rule for it to fail against, so the
> defect looked exactly like a decision.
>
> Verified: `go test ./... -count=1` green (45 tests, 0 failures), `gofmt -l .`
> and `go vet ./...` clean, tree diffed before/after. Five mutations, all caught
> — dropping `revoke` from `actionVerb`; `pending` → `pending` instead of
> `list-pending`; **deleting the unclassified-skip branch** (caught only by
> `TestUnclassifiedTrailingSegmentIsSkippedNotInvented`, which is why that
> synthetic-path test exists); dropping `permissions` from
> `listOnlyCollections`; making the derived collection set empty.
>
> **NOT released.** Every rename breaks a script written against any of the
> fourteen existing tags; tagging is a separate deliberate act.

> ~~**`cli/CHANGELOG.md` does not exist**~~ — **CLOSED 2026-08-28.** Written,
> going forward only. It opens by saying so: the pre-existing tags are not
> backfilled, and the file states why in its own text rather than leaving the
> gap to be discovered. The first entry is the *Unreleased* command-rename break
> above. Both pointers (`DECISIONS.md`'s header, this file's preamble) are now
> true as written, so neither needed correcting.
>
> **The item's own count was stale: there are FOURTEEN tags, not twelve** —
> `v0.2.0`–`v0.2.11`, `v0.3.0`, `v0.3.1`. It was written before the two `v0.3.x`
> releases and never revised, which is the argument against backfilling from
> memory stated by an item that was itself a memory. Recorded in the changelog's
> preamble rather than silently corrected.

