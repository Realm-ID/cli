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
- [ ] **Surface the approve-side error to the CLI poll** (cross-repo: `api/` +
  `cli/`). Today the CLI only ever sees `authorization_pending` until
  `expired_token`, so a failed approval (409 `approval_needs_app`,
  `login_failed`) is indistinguishable from a timeout. Needs the BFF to record a
  terminal failure reason on the device record and return it from
  `/auth/device/token`; then the CLI prints "approval failed: `<reason>`" instead
  of "expired before approval". **Contract change — the BFF half is owned in
  `api/TODO.md`; do that first.**
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

- [ ] **Service mode does not work against the issuer (pre-existing).** Tracked in
  the root `TODO.md` § Infra / dev-loop; restated here because it is CLI code.
  `resolveCredential` (`cmd/realm-id/commands.go:312-317`) sends the raw
  `REALM_ID_API_KEY` (`rk_live_…`) as `Authorization: Bearer` to
  `auth.realmid.dev`, on the in-code claim that the issuer accepts it as a
  platform credential. **It does not** — `requireAuth` runs the bearer through
  `LocalVerifier.Verify`, which rejects anything that is not a 3-part JWT, and
  `LookupByPresented` is reachable only from `/auth/login`, the user-api-key
  exchange and the integration mint. Confirmed live:
  `curl -H 'Authorization: Bearer rk_live_…' auth.realmid.dev/me` →
  `401 invalid bearer`. So only session mode (device flow → BFF passthrough) has
  ever worked. Fix: perform the `/auth/login` exchange and bear the resulting
  platform JWT; update the README's "issuer-direct" claim (~line 102) in the same
  change. **Not caused by ADR-089** — the CLI never held a refresh token here.

*(No open chores. The `gofmt -l` violation on `cmd/realm-id/main.go` was verified
clean 2026-07-28 and the item was removed; this line goes at the next sweep.)*
