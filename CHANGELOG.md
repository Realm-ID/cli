# Changelog — `realm-id` CLI

Release notes for consumers of the binary. **WHAT shipped** lives here, the
**WHY** lives in `DECISIONS.md`, and open work lives in `TODO.md`.

## This file starts on 2026-08-28, and the fourteen releases before it are not in it

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

## Unreleased

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
