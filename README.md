# realm-id

The agent-first CLI for the [RealmID](https://realmid.dev) identity platform (ADR-062).

gcloud-shaped, JSON-emitting, and driveable by humans **and** AI agents. `auth login`
uses the OAuth 2.0 Device Authorization Grant (RFC 8628), so a terminal or agent
authenticates by opening a link in your system browser — no embedded browser.

## Install

**Recommended — prebuilt binary (no Go toolchain, no repo access):**

```bash
curl -fsSL https://realmid.dev/cli/install.sh | sh
```

Downloads a checksum-verified binary for your OS/arch from GitHub Releases.
Pin a version or install dir: `… | sh -s -- --version v0.2.4 --bin-dir "$HOME/.local/bin"`.
Windows: download the `.zip` from the [Releases page](https://github.com/Realm-ID/cli/releases).

**Homebrew** (after the tap is published — see `TODO.md` §6):

```bash
brew install realm-id/tap/realm-id
```

**From source** (needs the Go toolchain):

```bash
go install github.com/Realm-ID/cli/cmd/realm-id@latest
```

## Usage

```bash
realm-id auth login              # authenticate via a browser link (device flow)
realm-id auth whoami             # show the current session identity
realm-id auth logout             # revoke + clear local credentials

realm-id config set platform <id>    # set the active platform (fills {id}/{pid})
realm-id config list                 # show the active configuration

# typed command tree — gcloud-shaped <resource> <verb>, generated from the spec
realm-id platforms list-mine
realm-id roles list --platform plt_abc
realm-id roles create --platform plt_abc --field name=editor --field description="Can edit"

# ADR-097 scope rename — realm-wide, one transaction, over every user API key
# cap in the realm. NOT reversible in general: where a key held both strings,
# the merge destroys what a reversal would need. ALWAYS dry-run first.
realm-id scopes rename --platform plt_abc --dry_run true \
  --field from=reports.read --field to=reports:read
realm-id scopes rename --platform plt_abc \
  --field from=reports.read --field to=reports:read
realm-id api-keys create --platform plt_abc --field label=provisioning   # mint
realm-id api-keys revoke --platform plt_abc --keyId ak_123                # rotate
# The verb is `revoke`, not `delete`: it sets revoked_at on a row that stays
# readable, and a replacement is one `create` away. It is the ONE exemption from
# the ADR-062 §5 destructive-verb filter (ADR-085 §8) — rotation is part of
# onboarding, not an irreversible act, and without it a partner who lost a key
# could not rotate from the CLI at all. Every other DELETE is still absent until
# machine-2FA exists; reach those with `realm-id api --method DELETE`.
#
# Minting is capped: a realm holds at most 2 ACTIVE platform keys (one steady
# state, one rotation slot) and at most 1 non-expiring — over it, create returns
# 409 too_many_api_keys. Keys now expire by default (90 days; `--field
# ttl_seconds=…` to choose, `--field non_expiring=true` for the one permanent
# slot).

# ADR-084 end-user API keys — LIST and REVOKE only.
#
# `user-api-keys create` is NOT a CLI command. ADR-097 §E made minting BFF-only:
# it needs a PLATFORM bearer escorting a user token (Authorization: platform +
# X-User-Token), and this binary holds a user token from the ADR-062 device
# flow. The verb is filtered out of the generated tree rather than left to 401
# at runtime — a broken command is worse than an absent one, because you cannot
# tell it from a missing capability. Mint through your own backend, or the
# console. (The endpoint still exists; it is BFFs that may call it.)
realm-id user-api-keys list --tenant ten_123 --uid usr_9
realm-id user-api-keys revoke --tenant ten_123 --uid usr_9 --id uak_456
# Revocation is the primary control for an end-user key (ADR-084 §9), so it is
# exempt from the destructive-verb filter for the same reason api-keys revoke is.
realm-id users list --tenant ten_123 --status active
realm-id users set-role --tenant ten_123 --uid usr_9 --field role:=\"owner\"

# create an org — owner is REQUIRED (ADR-073 Amendment C; owner_user_id is NOT
# NULL). Optionally bring your own tenant id + created_at for a verbatim import.
realm-id tenants create --platform plt_abc \
  --field display_name=Acme \
  --field owner:='{"email":"boss@acme.com","display_name":"Boss"}'
realm-id tenants describe --tenant ten_123 --output table

realm-id <resource>              # list a resource's verbs
realm-id schema                  # dump the OpenAPI contract (agent self-orientation)
realm-id api GET /me             # raw authenticated request through the BFF
realm-id version

# ADR-115 bring-your-own phone delivery (SMS via Twilio, WhatsApp via Meta).
# The provider secret (auth_token / api_key_secret / access_token) is write-only
# and is REFUSED as --field or --json (shell history, `ps`). Pipe it on stdin:
realm-id messaging update --channel sms --field account_sid=AC... \
  --field from_number=+1555... --secret-stdin < token.txt
# (--secret-stdin: sms -> auth_token, whatsapp -> access_token. The sms API-key
# pair goes as a JSON body on stdin. On a terminal with no secret given, a hidden
# prompt asks for it.) The provider checks the credentials before anything is
# stored; a refusal is 422 channel_validation_failed.
realm-id messaging describe --channel sms     # config + status, never a secret
realm-id messaging revoke   --channel sms     # remove (idempotent)
realm-id explain messaging                    # this guide
```

### Helper commands (hand-written, not generated)

Dispatched by name before the generated tree, so they never collide with a
resource and never pass through `skipDestructive()` (none of them writes).
All follow the same output rule as the typed tree: `--output json|text`, JSON
when stdout is not a terminal.

```bash
# Verify an access token against the realm's JWKS and explain every claim.
# Exit 0 = valid, 1 = invalid (errors listed), 2 = usage.
realm-id verify-token "$JWT"
realm-id verify-token --issuer http://auth.realmid.local --aud realmid "$JWT"

# Check a partner backend's RealmID config: one PASS/FAIL/SKIP line per check,
# the count of checks run, non-zero exit on any FAIL.
# [SERVICE MODE, removed by R6/R7 -- see "Planned" below: the rk_live_ key check goes.]
REALM_ID_API_KEY=rk_live_… realm-id doctor --realm <realm-id> [--issuer <url>]

# Validate a bulk user-import CSV offline, BEFORE any write (never calls the API).
# Columns: user_id,email,phone,role,display_name,provider,provider_uid,created_at
# (role required; email or phone required). Exit 0 = no errors, 1 = errors.
realm-id plan-import users.csv

# Look an error code up in the SDK's error reference (offline, embedded).
# Exit 0 = found (every row, with its section), 1 = unknown_error_code (with
# "did you mean" suggestions), 2 = usage.
realm-id explain missing_tenant_id
```

`explain` reads `cmd/realm-id/error-reference.md`, vendored from the sdk repo's
`docs/error-reference.md` at the commit pinned in `cmd/realm-id/ERROR_REFERENCE_CONTRACT`
(`source_sha` in the output). Re-vendor with `scripts/revendor-error-reference.sh <sdk sha>`;
`TestErrorReferenceMatchesItsPin` fails if the file and its pin disagree.

All helper commands print one JSON envelope: `{"command", "ok", "errors":
[{"code","message"}], ...}`; every failure has a stable snake_case `code`.

`doctor` exit codes: **0** every check PASS, **1** any FAIL, **3** no FAIL but at
least one SKIP (incomplete: skipped checks were not run, so this is not an
all-clear). With `--realm`, the key's minted token must name that realm in its
`iss` (`realm_mismatch` otherwise).

`verify-token` accepts access tokens only (mirroring the issuer's `AccessOnly`:
`typ` JWT/at+jwt, no `events` claim; it also refuses the issuer's internal MFA and
session-revoke challenge tokens) and answers `not_an_access_token` otherwise. It
always lists `not_checked`: the audience unless `--aud` is given, and revocation
(the CLI cannot see session state, so a revoked session still verifies until the
token expires).

`doctor` runs five checks: issuer health (asserts the `/.well-known/health` BODY
is `UP`, since it answers 200 even when degraded), the realm's OIDC discovery
document (its `issuer` must equal `<issuer>/<realm>`), JWKS non-empty with usable
RSA keys, `REALM_ID_API_KEY` accepted (the `platform_api_key` exchange), and the
minted token verifying against the JWKS. Without `--realm` (or
`$REALM_ID_REALM`) the discovery/JWKS checks SKIP; without a key the key checks
SKIP, each saying what to supply. The key is proven by the exchange rather than
a GET because no read endpoint is open to every platform key; a 403 from a
permission-gated read would be misreported as a bad key.

`plan-import` reports every problem with the issuer's own 1-based row number
(header excluded) plus the physical `file_line`, and a summary count. Rules come
from the issuer's import handler, not the spec text (phone is strict E.164). It
lists what it cannot check offline (role exists in the realm, seating authority,
collisions with existing users, single-tenant membership).

`verify-token` is RS256-only (the only algorithm the issuer signs with). It
derives the JWKS URL as `<issuer>/<realm id>/.well-known/jwks.json` where the
realm id is the tail of the token's `iss`, but **only if `iss` is under the
configured issuer** (`--issuer` > `$REALM_ID_ISSUER` > config > default); a token
naming any other host is refused before any request, so a forged `iss` cannot
steer the key fetch. No leeway is applied to `exp`/`nbf`.

### Typed command tree

The `<resource> <verb>` tree is **generated at startup from the embedded issuer
OpenAPI spec** (`cmd/realm-id/openapi.yaml`, vendored from `issuer/docs/swagger.yaml`),
so it stays in lockstep as the API evolves — re-sync with `go generate ./...` and
rebuild. Mapping: REST resource → noun, method → verb (`list`/`describe`/`create`/
`update` plus named sub-actions like `rename`, `set-role`, `claim`, `verify`).

- **Scope** — the active platform (`config set platform` or `--platform`) fills
  `{id}`/`{pid}`; `--tenant` fills `{tid}`/`{tenantId}`; other path params are
  required `--<name>` flags (e.g. `--uid`, `--roleId`).
- **Body** — `--json '<obj>'`, repeatable `--field k=v` (scalars are type-inferred;
  `key:=rawjson` injects a typed value), or JSON piped on stdin.
- **Output** — `--output json|table`; defaults to **table on a TTY, JSON when piped**
  so agents always get parseable output.
- **Where it talks** — the typed tree is the issuer's admin contract.
  **[SERVICE MODE, TODAY ONLY: R6/R7 remove everything in this bullet about `rk_live_` and
  issuer-direct; see "Planned" below.]** With
  `REALM_ID_API_KEY=rk_live_…` set it runs **issuer-direct**
  (`auth.realmid.dev`, ADR-062 §4 Service mode): the key is **exchanged once per
  invocation** for a short-lived platform JWT via
  `POST /auth/login {grant_type: platform_api_key}`, and that JWT is the bearer.
  The raw key is never sent as a bearer — the issuer rejects any bearer that is
  not a 3-part JWT, so doing so answers `401 invalid bearer`. Per ADR-089 the
  exchange returns **no refresh token** (the key itself is the renewable
  credential), so the token is held in memory for the process and never written
  to disk. Without the key, commands fall back to the `auth login` session
  bearer and route through the BFF's `/api/*` passthrough.
- **Collisions** — where a hierarchical API flattens to the same `resource verb`
  (e.g. platform- vs tenant-scoped `identity-providers list`), the broadest-scope
  variant wins; the narrower ones stay reachable via `realm-id api`.
- **Unrecognised segments** — a trailing path segment the generator can classify
  as neither a collection nor an action produces **no command at all** (use
  `realm-id api`), and fails the build's own test until someone names it. It is
  not guessed at: until 2026-08-28 it was, which is how thirteen action segments
  each became a top-level "resource" you invoked as `realm-id revoke create`.

`auth login` opens the approval link in your default browser (best-effort, only on
an interactive terminal) and prints it as a fallback; the link already carries the
one-time code, so you never type or match a code. Re-running `auth login` supersedes
any earlier run still waiting — the older poller stops on its next tick. Set
`REALM_ID_NO_BROWSER=1` to suppress the auto-open (headless/agent/CI runs).

> **[SERVICE MODE note, TODAY ONLY: R6/R7 remove the `rk_live_` advice below; unattended runs
> become a user API key (`uk_live_`) or workload identity.]**
> **Provisioning runs:** the device-login session bearer is **short-lived**, so a
> long multi-step provisioning sequence can outlive it (you'll start getting `401`
> mid-run). Either re-run `auth login` when that happens, or for an unattended
> provisioning run set `REALM_ID_API_KEY=rk_live_…` (Service mode, no session
> expiry). Also avoid running `auth login` in **two terminals at once** — you may
> approve one run's code while the *other* run's poller is the one you're watching,
> which looks like a hang. One run, one code, poll until it mints.

Config lives at `~/.config/realm-id/config.json` (mode `0600`; it holds the session
bearer). Overrides: `REALM_ID_BFF`, `REALM_ID_ISSUER`, `REALM_ID_API_KEY`,
`REALM_ID_CONFIG`, `REALM_ID_NO_BROWSER`.

## Status

Shipped: `auth` (device flow), `config`, the generic `api` passthrough, `schema`,
and the **typed command tree** (platforms, tenants, users, invitations, api-keys,
user-api-keys, roles, federation-bindings, origins, domains, identity-providers,
audit-events, + `admin …`) generated from the OpenAPI spec (ADR-062 §1).
Destructive verbs
(delete / signing-key rotate / suspend / ownership transfer) are intentionally
absent pending machine-2FA (ADR-062 §5).

Exit codes today (for agents): `0` ok · `2` usage · `4` conflict · `5` not-found ·
`7` forbidden · `1` other. The planned set (G15) is under "Planned" below.

## Planned: split signing, BFF-only CLI (NOT BUILT YET)

Everything in this section is **planned and the code does not exist yet**. It is specified in
`plans/2026-10-27-split-signing-v2.md` § 5.1 (rulings R6, R7, R24, R40, R41; G13-G15) in the
root repo. Until it ships, only the sections above describe the binary.

**Run `init` yourself, not via an agent (J5).** `signing-keys init` generates the realm's private
signing key and splits it; the half you keep must never pass through an agent's context, shell
history or logs. Run it in your own terminal and send the share to a secret store you control.

**What R6/R7 remove (the service mode described above):** `REALM_ID_API_KEY=rk_live_…` no longer
signs the CLI in, and the typed tree no longer runs issuer-direct (ADR-062 § 4 Service mode).
`rk_live_` in `REALM_ID_API_KEY` will exit 2 with a message pointing at a user API key
(`uk_live_`) or workload identity. The CLI is BFF-only.

Scope of the `rk_live_` refusal (R57): the key session accepts only `uk_live_` (service accounts
have no API keys), and the CLI refuses `rk_live_` locally on the USER-KEY path only, with the
migration message. `realm-id doctor` keeps working with `REALM_ID_API_KEY=rk_live_…` until service
mode itself is removed. A key session gets an access token only, no refresh token (R58: the key is
presented again when the token expires, so a revoked key stops on its next use).

Credentials:

- `realm-id auth login` (device flow; TOTP in the page, R41).
- `REALM_ID_API_KEY=uk_live_…` gives an automatic key session.
- `realm-id auth workload [--provider github] [--audience https://api.realmid.dev]` for CI (R7).

Planned verbs outside the signing family:

```bash
realm-id user-api-keys create --tenant T --label L --permissions p1,p2 [--org-scope …] \
  [--expires 30d] --out FILE        # stdout refused off a TTY; also list|describe|update|revoke
realm-id platforms update --integration-token-endpoint https://…   # R47 (verb proposed)
```

The `signing-keys` family (G14; `init`, `pin` and `rotate` are kept in it, read as non-exhaustive):

| Verb | What it does |
|---|---|
| `signing-keys init --realm R [--kid K] --share-to <target>` | Offline keygen and dealer split (R5). Writes the bundle and the public-only list (R39); prints `{kid, fingerprint, n_bits, written_to}`. |
| `signing-keys upload --realm R --bundle … [--overlap 24h]` | Sends `n`, `e`, `d_ri`, `kid`, then drops `d_ri` locally. Starts `overlap_pending` (R36). Needs the human owner session or a `signing_keys:rotate` key (G9). Overlap plus extensions at most 7d (G8). |
| `signing-keys status --realm R` | Custody, state, overlap, per-kid fingerprint and last handshake, rotation due/overdue, emergency rotations today. |
| `signing-keys extend --realm R --by D` | Extend the overlap (R36, G8). |
| `signing-keys cancel --realm R` | Cancel the overlap; deletes the uploaded split key and the realm stays full (G8). |
| `signing-keys pin --realm R` | Print the public-only list or pin snippet for verify-only and hand-pinned services (R23). |
| `signing-keys rotate` | Rotation from CI with a `signing_keys:rotate` key (R9). |
| `signing-keys recover` | Recovery from an env source only; prints the new value (R39). |
| `signing-keys downgrade [--cosign-from <store>] [--cancel]` | Human owner only. Co-signed is immediate, otherwise 24h (R38). |

`--share-to` targets (G13): `gsm:<secret>`, `file:<path>`, `redis:<url>` and `stdout`. `stdout`
is accepted only when asked for explicitly, and prints `::add-mask::` first (R9). `redis:`
encrypts the share with a key read from `REALM_ID_SHARE_KEK`. There is no manual handshake verb:
the handshake is SDK-only (G9).

Other planned behaviour: dangerous verbs (`tenants delete`, `tenants transfer-owner`,
`users deactivate`, `platform-keys create|revoke`) are human-session only with step-up; a key or
workload session exits 8 (R40). The CLI reads `x-realmid-human-only` from its vendored spec (G12).
Per R55 the issuer refuses EVERY key session (capped or uncapped `uk_live_`) on its never-via-key
list; per R56 a platform token may still manage service accounts, so SA create/change/delete is a
"no API key" operation, not a "person must perform" one.
Also `platform bootstrap [--split]`, `doctor --split`, `explain <code>` rows for the new codes,
`help --json`, completion, `export-config` and `--dry-run` (C5). Removed against the earlier
draft: `activate`, `repin`, `retire`, `emergency-retire`, and the `signing split …` family name.

Planned exit codes (G15): `0` ok · `1` error · `2` usage · `3` incomplete · `4` conflict ·
`5` not found · `6` unauthenticated (401; split out of 7) · `7` forbidden · `8` needs a human
(R40) · `9` split signing (share unavailable, untrusted kid, handshake failed). Moving 401 from
7 to 6 is a breaking change and goes under `### Breaking` in the CHANGELOG.

Planned error codes (G15 adopts v2 § 5.3; names marked * were proposed in W0 and the owner may
rename them): `split_signing_required` 409, `realm_not_split` 409, `platform_key_not_a_user_key`
400, `signing_key_unavailable` 409, `needs_human` (exit 8), `split_realm_rotation_is_a_ceremony`
409, `not_available_to_api_keys` 403, `idempotency_key_reused` 422, `idempotency_in_progress` 409,
`assertion_replayed` 409, `split_key_size_unsupported`* 400, `split_kid_in_use`* 409,
`rotation_lease_held`* 409, `emergency_rotation_cap_reached`* 429, `not_in_overlap`* 409,
`install_pass_invalid`* 401, `installation_not_found`* 404, `integration_endpoint_missing`* 409,
`integration_endpoint_unverified`* 422, `integration_grant_requires_install_pass`* 400,
`downgrade_already_pending`* 409, `login_grant_removed`* 400 (release B only).

`explain` answers these only after the sdk error reference carries them and
`cmd/realm-id/error-reference.md` is re-vendored (see above); until then it returns
`unknown_error_code`.
