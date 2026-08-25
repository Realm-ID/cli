# TODO — cli (`realm-id`)

Open work only; shipped items live in `CHANGELOG.md` / git tags.

> **Re-verified 2026-08-03** against the tree — all four items still stand, with
> the cited line numbers refreshed: `resolveCredential`
> (`cmd/realm-id/commands.go:312-317`) still returns the raw `rk_live_…` as the
> bearer for the issuer, and `authWhoami` (`cmd/realm-id/main.go:501-512`) still
> prints `/me` verbatim with no `exp` decode.

---

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

## Broken today

> ⚠️ **THIS SECTION IS EMPTY — everything below is a FIXED-note kept for its
> findings.** Recorded 2026-08-21 because the heading alone reads as a live
> pool of bugs, and it was mistaken for one during that day's cross-file sweep,
> before the sweep actually reached the file. The open CLI work is the two
> Device-flow DX items above.

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

- [ ] **Fourteen commands are mis-derived as top-level "resources" whose verb is
      `create`** — `cmd/realm-id/spec.go`, `deriveCommand`. A trailing static
      segment is treated as a collection noun unless `actionVerb` names it, so
      every action segment absent from that set becomes its own top-level
      resource: `realm-id revoke create` revokes a service account,
      `realm-id import create` imports users, and likewise `deactivate`,
      `delink`, `disable`, `enable`, `hand-back`, `leave`, `pending`, `request`,
      `reset-handle`, `revoke-all`, `tenant-choice`. Pre-dates ADR-097; found
      2026-08-25 while diffing the tree for the `0.32.0` re-vendor, when the new
      `/scopes/remove` path would have become the fourteenth. **Fixing one of
      fourteen was tried and reverted** — it makes the tree less consistent while
      reading as a fix. Each needs its own naming decision, and every rename is
      breaking for a shipped binary, so this wants one deliberate pass.
      Currently pinned as-is (and marked "not endorsed") by
      `TestTopLevelResourceGroupsAreReviewed`.

- [ ] **`cli/CHANGELOG.md` does not exist** — `DECISIONS.md`'s header and this
      file's own preamble both point at one ("shipped items live in
      `CHANGELOG.md` / git tags"), and there are twelve tags with no release
      notes. Either write it going forward or correct both pointers to say git
      tags are the only record. Not backfilled 2026-08-25: reconstructing twelve
      releases from commit subjects yields a document that looks authoritative
      and is not.

