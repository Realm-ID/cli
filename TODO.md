# TODO — cli (`realm-id`)

> **Open and actionable only.** Work that is decided-but-not-now lives in
> [`BACKLOG.md`](BACKLOG.md); anything needing an ADR or an owner ruling lives
> in [`OPEN-QUESTIONS.md`](OPEN-QUESTIONS.md); closed and retired records live
> in [`TODO-ARCHIVE.md`](TODO-ARCHIVE.md). An entry belongs here only if
> someone could pick it up today and finish it.


Open work only; shipped items live in `CHANGELOG.md` / git tags.

> **Re-verified 2026-09-13 against the tree. The previous note here was half
> stale and is replaced.** It was dated 2026-08-03, claimed "all four items
> still stand", and survived both the fix that closed one of them and the
> sweeps that closed two others.
>
> - **CLOSED — `resolveCredential` does NOT send the raw `rk_live_…` as a
>   bearer.** `cmd/realm-id/commands.go:325` performs the ADR-051 exchange
>   (`POST /auth/login {grant_type: platform_api_key}`) and bears the returned
>   platform JWT; the raw key is only ever the `api_key` BODY field. Fixed
>   2026-08-05 — **two days after the note asserting otherwise was written.**
>   The record is in [`TODO-ARCHIVE.md`](TODO-ARCHIVE.md), including the part
>   worth remembering: the old `TestResolveCredential` asserted
>   `bearer == "rk_live_1"`, so it restated the implementation and would have
>   failed the moment anyone fixed the bug it was protecting.
> - **STILL OPEN — `authWhoami` (`cmd/realm-id/main.go:534`) prints `/me`
>   verbatim with no `exp` decode.** The CLI decodes `exp` NOWHERE. Note the
>   partial mitigation, so it is not re-derived: `sessionHint` (`:523`) prints
>   a re-login hint, but only on a **401** carrying `session_expired`,
>   `session_missing` or `session_revoked` — reactive, after expiry has
>   already bitten, and it never says when a live session will expire.
>
> Both line numbers in the old note had also drifted (`commands.go:312-317`
> and `main.go:501-512` no longer point at either function).


## Device-flow DX (Traide integration feedback, 2026-06-29)

Surfaced provisioning the Traide prod realm via CLI device login
(`../tally-helper/docs/realmid-integration-process-feedback-2026-06-29.md` §1).
The docs side is handled (`README.md`: re-auth, `REALM_ID_API_KEY` for long runs,
warning against concurrent `auth login`); these are the code fixes.
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

- [ ] **The vendored pin is verified REAL and CONSISTENT, never CURRENT** —
      nothing notices when it falls behind the issuer's newest release.
      ⚠️ **RE-SCOPED 2026-09-13. This item used to read "No test compares the
      vendored `info.version` against the issuer's", and that is no longer
      true** — it was written before the 2026-09-06 work and never revisited.
      What exists today:
  - `scripts/revendor-spec.sh` vendors from a **release tag**, replacing the
    `//go:generate cp ../../../issuer/docs/swagger.yaml` that copied from the
    sibling WORKING TREE and could vendor an unreleased or mid-edit spec.
  - `cmd/realm-id/spec_contract_test.go` — the embedded bytes must hash to
    what `ISSUER_CONTRACT` names, the pin must be a release tag, and a real
    command tree must still come out of the spec (the non-vacuity half).
  - The umbrella's `scripts/issuer-pin-parity.py` (in its `make check`) —
    and it **does** reach the issuer, contrary to what this item claimed: it
    verifies the pinned tag EXISTS in `issuer/` and that its
    `docs/swagger.yaml` hashes to the recorded `spec_sha256`. It also holds
    `api/` and `cli/` to the SAME issuer release, which matters because every
    generated command is sent through the BFF's `/api/*` passthrough.

      **The residual gap, stated precisely.** All three check that the pin is
      real and internally consistent. **None checks that it is the NEWEST
      issuer release.** A live instance as of 2026-09-13: `ISSUER_CONTRACT`
      pins `issuer_tag=v0.121.1` while the issuer is tagged **`v0.122.0`**.
      That is benign *only* because `docs/swagger.yaml` is byte-identical
      across those two tags — verified, both blobs hash
      `5e139f99aa68c3b2…`. **Had `v0.122.0` touched the spec, every gate above
      would still be green and this CLI would ship an outdated command tree.**
      For this repo that is a wrong PROGRAM, not a wrong document, because
      `buildCommands()` derives the whole tree from the spec at startup.

      **The obvious closer**: have `issuer-pin-parity.py` also compare the pin
      against the issuer's newest tag and **WARN, not fail**, when it is
      behind. Being behind is legitimate — re-vendoring is a decision, not a
      build step (which is why `go generate` deliberately does not do it).
      Being *unknowingly* behind is the defect. Keep it local-only for the
      same reason the rest of that script is: this umbrella's CI checks out
      only itself, so a CI copy could only ever report "NOT CHECKED".
