# Decisions — Realm-ID/cli

Rationale log for the `realm-id` CLI. WHAT-shipped lives in git/CHANGELOG; this
file records WHY. See the root `Realm-ID/project` DECISIONS.md for cross-cutting
context.

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
