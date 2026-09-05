# Changelog — `realm-id` CLI

Release notes for consumers of the binary. **WHAT shipped** lives here, the
**WHY** lives in `DECISIONS.md`, and open work lives in `TODO.md`.

## v0.3.6 — the seat guard reaches `role-templates update` (2026-09-05)

### Changed

- Re-vendored the issuer spec `0.44.0` → `0.46.0` (issuer `v0.121.0`/
  `v0.121.1`, ADR-101). `role-templates update` gains an optional
  `--override_seated` flag; no other command, flag, or required-ness changed.
  `role-templates` stays create/list/update, no `delete` verb (ADR-062 §5).
  See `DECISIONS.md`.

## v0.3.5 — the spec catches up six issuer releases (2026-09-04)

### Changed

- Re-vendored the issuer spec `0.38.0` → `0.44.0` (the measured gap was six
  releases, not the five the tracking item's title said). Command tree
  unchanged at the group/verb/path level — 3 endpoints gained optional
  pagination flags (`--cursor`/`--limit`) only, no breaking changes. See
  `DECISIONS.md`.

## v0.3.4 — credential commands reach the CLI (2026-09-01)

Re-vendors the issuer spec `0.37.0` → `0.38.0` (ADR-102/103/104/105), which
prod has been serving since issuer `v0.116.0`. As with the last two releases the
CLI is tagged AFTER the deploy, so prod served the surface before the CLI
described it.

### Added — `me set-password` and `users set-credentials` (ADR-104)

The re-vendor introduced two trailing segments the derivation could not
classify, and `TestEverySpecSegmentIsClassified` failed rather than quietly
dropping them — which is the guard working. An unclassified operation is ABSENT
from the typed tree and reachable only through `realm-id api`.

Both are classified the same way as the existing `role` / `status` / `owner`
segments: a PUT that REPLACES a named sub-resource of its parent.

- `me set-password` — `PUT /me/password`. The SELF route: the caller changes
  their own password and must present `current_password` unless none is set, so
  the CLI cannot use it to act on anyone else, and an agent holding the session
  already holds the authority it confers.
- `users set-credentials` — `PUT /tenants/{tid}/users/{uid}/credentials`, the
  admin counterpart. ⚠️ What it writes is an ASSERTION, not a proof: the
  credential carries `must_change` and the holder's next login answers
  `403 password_must_change` until they replace it. An operator reaching for
  this to "log in as" somebody does not get that, by design.

### Unchanged — no command for the rest of the release

ADR-102's `product_roles` is a request FIELD on `/auth/token`, not a route;
ADR-103's `delivery_mode` likewise. ADR-105 removes wire fields and adds
nothing. None of them grows the command tree, and none needed a decision.

## v0.3.3 — the role vocabulary reaches the CLI (2026-08-30)

- Spec re-vendored to issuer `0.37.0`, which adds the **`role-templates`**
  resource (ADR-101 D1's write side): `create`, `list`, `update`.
- Exposing it was a deliberate review decision, not a side effect of the
  re-vendor — `TestTopLevelResourceGroupsAreReviewed` fails on any NEW resource
  group until someone decides. The reasoning is recorded there: the surface is
  base-realm-GATED rather than RI-private, so a partner reaching it gets an
  honest `403` naming the ADR-097 replacement, and D1 exists precisely so
  RealmID can add a role without a release — which an agent-first CLI is the
  natural place to script.
- No `delete` verb, matching `roles`: ADR-062 §5 skips a destructive DELETE that
  is not a revocation.

### This file starts on 2026-08-28, and the fourteen releases before it are not in it

`v0.2.0` through `v0.3.1` — fourteen tags — shipped with no release notes, and
they are **deliberately not backfilled**. Reconstructing them from commit
subjects produces a document that reads as authoritative and is not: a commit
subject records what a diff did, not what changed for someone running the
binary, and nobody would be able to tell the reconstructed entries from the
observed ones afterwards. For anything tagged before this file existed, the
record is `git log <tag>` plus the dated entry in `DECISIONS.md`.

(`TODO.md` and the item that asked for this file both said "twelve tags". There
are fourteen: `v0.2.0`–`v0.2.11`, `v0.3.0`, `v0.3.1`. The count was written
before the two `v0.3.x` releases and was never revised — which is itself the
argument against backfilling from memory.)

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)-ish, newest
first. Versions are the git tags that trigger `release.yml`.

---

## v0.3.2 — the spec re-vendor for ADR-101's role set (2026-08-30)

### Changes

### Changed — ADR-101: the role set is RealmID's, and one command disappears

Spec re-vendor to issuer `0.36.0`. The command tree derives from the embedded
OpenAPI contract at runtime, so this is what actually changed in the binary:

- **`starter-roles create` is GONE.** It opted a realm into the `admin`/`viewer`
  templates. `admin` is now part of the set every realm receives and `viewer`
  no longer exists, so the endpoint could only refuse; it was deleted
  server-side rather than left returning 400, and answers `404`.

- **`roles` SURVIVES with all four verbs**, and that is worth stating because
  ADR-101's own consequences list predicted the resource would disappear. It
  does not: authoring is base-realm-GATED, not deleted. `roles create` /
  `update` / `delete` / `rename` still exist and now answer
  `403 role_authoring_retired` for every realm but RealmID's own, while
  `roles list` and `roles disable` / `enable` are unchanged and open to every
  realm owner. A CLI that hid them would be lying about the API.

- **`roles create` and `roles update` no longer accept `--field
  required_mfa_methods=…` or `--field can_invite_roles=…`.** Both fields left
  the contract with the columns behind them; the issuer now ANNOUNCES an ignored
  body key via a `Warning: 299` header (issuer v0.108.0), so a stale script gets
  told rather than silently dropped.

- **`integration-installations create` takes `permissions`, not `role_id`.** An
  installation states the authority it confers as an array of ADR-074 catalog
  permissions, bounded by the installing actor's own authority (ADR-101 D7).

### Changed — BREAKING: sixteen generated commands are renamed

The typed command tree derives `<resource> <verb>` from the embedded OpenAPI
spec. A trailing static path segment that `actionVerb` did not name was treated
as a collection noun, so each such action segment became its own **top-level
resource** whose verb was `create`. Thirteen of them shipped that way. They now
derive as `<parent collection> <action>`, which is the shape the rest of the
tree already used (`roles rename`, `users set-role`, `domains claim`).

| Old command | New command | Operation |
|---|---|---|
| `revoke create` | `service-accounts revoke` | `POST /tenants/{id}/service-accounts/{said}/revoke` |
| `deactivate create` | `service-accounts deactivate` | `POST /tenants/{id}/service-accounts/{said}/deactivate` |
| `reset-handle create` | `service-accounts reset-handle` | `POST /tenants/{id}/service-accounts/{said}/reset-handle` |
| `revoke-all create` | `sessions revoke-all` | `POST /platforms/{id}/sessions/revoke-all` |
| *(unreachable)* | `sessions revoke` | `POST /tenants/{id}/users/{uid}/sessions/revoke` |
| `import create` | `users import` | `POST /tenants/{id}/users/import` |
| `hand-back create` | `users hand-back` | `POST /tenants/{id}/users/{uid}/hand-back` |
| `disable create` | `integrations disable` | `POST /platforms/{id}/integrations/{iid}/disable` |
| `enable create` | `integrations enable` | `POST /platforms/{id}/integrations/{iid}/enable` |
| *(unreachable)* | `roles disable` | `POST /platforms/{id}/roles/{roleId}/disable` |
| *(unreachable)* | `roles enable` | `POST /platforms/{id}/roles/{roleId}/enable` |
| `delink create` | `contacts delink` | `POST /tenants/{id}/users/{uid}/contacts/{contactId}/delink` |
| `leave create` | `memberships leave` | `POST /me/memberships/{tenantId}/leave` |
| `tenant-choice create` | `me tenant-choice` | `POST /me/tenant-choice` |
| `request create` | `sso-domains request` | `POST /platforms/{pid}/tenants/{tid}/sso-domains/{domain}/request` |
| `pending list` | `domains list-pending` | `GET /domains/pending` |

**Three operations marked *(unreachable)* were not renamed — they were
absent.** `roles disable` and `roles enable` collided with the `integrations`
pair on the bogus group name `disable`/`enable`, and `sessions revoke` collided
with the service-account revoke on `revoke`; the collision tie-break dropped one
of each. Fixing the grouping puts them in different groups, so all six now
generate. The tree goes from 94 commands in 44 groups to **97 commands in 35
groups**.

**Migration.** Every rename is a mechanical `s/<segment> create/<parent>
<segment>/` in scripts; `realm-id <resource>` lists a group's verbs and
`realm-id --help` lists the groups. Nothing about the underlying HTTP call
changed — same method, same path, same flags — so `realm-id api` invocations are
unaffected.

### Fixed

- An unrecognised trailing path segment now produces **no command** instead of a
  guessed one. It is reported by `TestEverySpecSegmentIsClassified`, so a future
  spec re-vendor that introduces one fails the build until someone names it —
  the review that did not happen the first time. The operation stays reachable
  via `realm-id api`.

### Not released

**No tag is cut for this.** The renames break every shipped binary's command
surface and there is nothing else queued behind them; releasing is a separate,
deliberate act. Rationale: `DECISIONS.md` 2026-08-28.
