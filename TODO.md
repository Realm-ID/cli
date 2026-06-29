# cli — punch list

## Device-flow DX (from Traide integration-process feedback, 2026-06-29)

Both surfaced provisioning the Traide prod realm via the CLI device login
(`../tally-helper/docs/realmid-integration-process-feedback-2026-06-29.md`
§1). The docs side is handled (`README.md`: re-auth / `REALM_ID_API_KEY`
for long runs, warn against concurrent `auth login`); these are the
underlying code fixes.

- [ ] **Surface token expiry in `realm-id auth whoami`** — the device-login
  session bearer is short-lived (Traide saw ~38 min) and nothing shows when
  it expires, so a long provisioning sequence (claim → verify → roles →
  bindings → config) hits a `401` mid-run with no obvious cause. Decode the
  stored bearer's `exp` claim and print remaining lifetime / "expires at" in
  `auth whoami` (CLI-only, `cmd/realm-id`). Small.

- [ ] **Bind the `/device` approval page to a specific `device_code`**
  (cross-repo: issuer + `ui/web`, not just CLI). The approval page doesn't
  show *which* run/code it's authorizing, so running `auth login` in two
  terminals at once → approving one run's code while watching the other's
  poller looks like an indefinite hang (`authorization_pending` forever).
  Traide filed false "STILL-BROKEN" bug reports over exactly this
  self-inflicted race. Fix: have the approval page display/confirm the
  `device_code` (or `user_code`) being approved, and/or surface "this code
  was already consumed by another session" instead of a silent pending.
  Touches the issuer `/auth/device/approve` surface + `ui/web/src/main.tsx`
  `/device` branch. (Related: root `TODO.md` "No post-deploy smoke check for
  ui/web /device".)
