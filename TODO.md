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

- [ ] **Surface token expiry in `realm-id auth whoami`** — the device-login
  session bearer is short-lived (Traide saw ~38 min) and nothing shows when it
  expires, so a long provisioning sequence (claim → verify → roles → bindings →
  config) hits a `401` mid-run with no obvious cause. Decode the stored bearer's
  `exp` and print remaining lifetime / "expires at". CLI-only
  (`cmd/realm-id`), small.
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

- [ ] **Bind the `/device` approval page to a specific `device_code`**
  (cross-repo: issuer + `ui/web`, not CLI-only). The approval page doesn't show
  *which* run/code it's authorizing, so running `auth login` in two terminals and
  approving one while watching the other's poller looks like an indefinite hang
  (`authorization_pending` forever) — Traide filed false "STILL-BROKEN" reports
  over exactly this self-inflicted race. Fix: have the page display/confirm the
  `device_code` (or `user_code`) being approved, and/or surface "this code was
  already consumed by another session" instead of silent pending. Touches the
  issuer `/auth/device/approve` surface + `ui/web/src/main.tsx` `/device` branch.
  *(Partially mitigated: `cli/v0.2.7` added a hard OS-lockfile singleton for
  `auth login`, so the CLI itself can no longer produce two live codes on one
  machine. The multi-machine / stale-tab case remains.)*

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
