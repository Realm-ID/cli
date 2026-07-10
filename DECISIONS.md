# Decisions — Realm-ID/cli

Rationale log for the `realm-id` CLI. WHAT-shipped lives in git/CHANGELOG; this
file records WHY. See the root `Realm-ID/project` DECISIONS.md for cross-cutting
context.

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
