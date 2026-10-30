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
# `user-api-keys create` is a hand-written verb (the mint is filtered out of the generated tree):
# a person's session only, with step-up via --totp. See "Split signing" below.
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
# doctor still reads an rk_live_ platform key (R57); the typed tree no longer accepts one.
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
- **Where it talks** — always the BFF's `/api/*` passthrough; the CLI never calls the issuer
  (R6, R63). A person's `auth login` session, a `uk_live_` key in `REALM_ID_API_KEY` (exchanged
  once per process at `POST /auth/key-session`, key in the body) or a CI `auth workload`
  session is the bearer. `rk_live_` in `REALM_ID_API_KEY` exits 2 (see below).
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

> **Provisioning runs:** the device-login session bearer is **short-lived**, so a
> long multi-step provisioning sequence can outlive it (you'll start getting `401`, exit 6
> mid-run). Either re-run `auth login` when that happens, or for an unattended run set
> `REALM_ID_API_KEY=uk_live_…` (a key session) or use `auth workload` in CI. Also avoid running `auth login` in **two terminals at once** — you may
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

Exit codes (for agents): see "Split signing" below.

## Split signing and the BFF-only CLI

Specified in `plans/2026-10-27-split-signing-v3.md` § 3.9 and § 7.2 (rulings R6, R7, R24, R40, R41,
R55, R63, R72-R74, R92; G13-G15, G19; owner rulings 2026-10-29 C5#1, C5#4, C5#6) in the root repo.

**Run `init` yourself, not via an agent (J5).** `signing-keys init` generates the realm's private
signing key and splits it; the half you keep must never pass through an agent's context, shell
history or logs. Run it in your own terminal and send the share to a secret store you control.

**No service mode (R6/R7).** The CLI never calls the issuer. `REALM_ID_API_KEY=rk_live_…` no
longer signs anything in: it exits 2 offline with a message pointing at `user-api-keys create`
(`doctor` keeps reading `rk_live_`, R57). Every call goes through the BFF `/api/*` passthrough.

Credentials:

- `realm-id auth login` (device flow; the person's session).
- `REALM_ID_API_KEY=uk_live_…` becomes a **key session** (`POST /auth/key-session`, the key in the
  body, an access token only, R58).
- `realm-id auth workload [--audience https://api.realmid.dev]` in GitHub Actions: the runner's OIDC
  token (`ACTIONS_ID_TOKEN_REQUEST_URL/_TOKEN`) becomes a **workload session**
  (`POST /auth/workload-session`), stored in the config file with its actor role.

**Human-only verbs (R40, R73).** `user-api-keys create` and `signing-keys
upload|extend|cancel|downgrade|rotate|recover` need a person's session: in a key or workload
session they exit 8 with `{"error":{"code":"needs_human",…}}` on stderr and send NOTHING, not
even the key exchange. `platform-keys create` is the one exception: a workload session whose actor
role is `platform_mgmt_api` may run it (R72). `signing-keys status` and `pin` are reads and run in
any session. A `412` step-up on a mutation takes `--totp CODE` (or a hidden prompt on a terminal),
verifies it at `/auth/mfa/verify` and retries the same request; off a terminal with no code the
exit is 3. The verify REPLACES the session: the BFF mints a new `session_token` and deletes the old
one, so the CLI adopts the verify body's `session_token` (saved to config for an `auth login`
session) and the retry carries THAT token. [R41 amendment, owner 2026-10-30; SPEC, NOT BUILT. A
`REALM_ID_API_KEY` key session is never written to config.]

**Picking a workspace that needs MFA (R41 amendment, owner 2026-10-30; SPEC, NOT BUILT).**
`realm-id config set tenant <id> [--totp CODE]` re-pins the session through `POST /switch-tenant`.
When that workspace needs MFA the BFF answers `412 mfa_required` with a challenge; the CLI then
takes the code exactly as a step-up does (`--totp`, else the hidden `TOTP code: ` prompt on a
terminal; off a terminal with no code: "error: this operation needs a TOTP code (step-up); pass
--totp <code>", exit 3), verifies it at `/auth/mfa/verify`, ADOPTS the returned `session_token`
into config (the old one is gone), saves the tenant, and prints `Active tenant: <id>`. A wrong
code prints the server's error and changes nothing. This is the same whether the session was
approved with "Choose later in the CLI" (unpinned) or is switching from one workspace to another.
There is no interactive numbered pick after `auth login`: a multi-workspace login still lists the
workspaces and prints `Select one with:  realm-id config set tenant <id>`, so the CLI stays
scriptable (ADR-062).

```bash
realm-id user-api-keys create --tenant T --label L (--permissions p1,p2 | --uncapped) [--user U] [--ttl-seconds N] [--totp 123456]
realm-id platform-keys create --realm R --label L [--totp …]
```

`user-api-keys list|describe|revoke` stay the generated verbs.

The `signing-keys` family (G14):

| Verb | What it does |
|---|---|
| `signing-keys init --realm R --kid K --share-to <target> [--out DIR]` | Offline keygen and dealer split (R5). Writes `bundle.json` (RealmID's half) and `public.json` (a public-only standard JWKS, R39) to DIR (default `.`); prints `{kid, fingerprint, n_bits, written_to}`. |
| `signing-keys upload --realm R --bundle … [--overlap 24h]` | Sends `n`, `e`, `d_ri`, `kid`, then drops `d_ri` from the bundle file. The FIRST upload takes no lease (C5#1). Human only. |
| `signing-keys status --realm R` | The custody document, as the server returns it. |
| `signing-keys extend --realm R --by D` / `cancel --realm R` | Extend the overlap / cancel it (also allowed in `overlap_pending`, C5#2). |
| `signing-keys pin --realm R` | Print the public-only standard JWKS `{"keys":[…]}` for the base-trust secret (C5#6; a bare array is refused downstream). |
| `signing-keys rotate --realm R --share-to T [--kid K] [--overlap D]` | Takes the rotation lease itself (`reason: rotation`), deals a new key, stores the new share in T beside the old one, and uploads with `lease_id` (C5#1). `409 rotation_lease_held` exits 4 and uploads nothing. |
| `signing-keys recover --realm R [--kid K] [--share-to T]` | The same with `reason: recovery` (R92); prints the new bundle masked on stdout unless T is given (R39). The emergency cap (`429`) exits 1. |
| `signing-keys downgrade --realm R [--cosign-from file:PATH] [--kid K] [--cancel]` | With `--cosign-from`: takes the downgrade's OWN challenge (`…/split/downgrade/challenge`, C5#4), completes it with the partner half and sends `{cosig}` (immediate). Without: the 24h path, no challenge. `--cancel` deletes a pending downgrade. |

`--share-to` targets (G13): `file:<path>` (a sealed envelope v1, AES-256-GCM; needs
`REALM_ID_SHARE_KEK`, 32 bytes base64url without padding) and `stdout` (explicit only; prints
`::add-mask::` first, R9). There is no default target. **`gsm:` and `redis:` stores are SDK add-ons
this CLI build does not carry**: they exit 2 with a message; write to `file:` and move the sealed
file. There is no manual handshake verb (G9). `activate`, `repin`, `retire`, `emergency-retire` and
the `signing split …` family are gone (exit 2).

Exit codes (G15): `0` ok · `1` error · `2` usage · `3` incomplete · `4` conflict · `5` not found ·
`6` unauthenticated (401; split out of 7) · `7` forbidden · `8` needs a human (R40) · `9` split
signing (share unavailable, cosignature or `handshake_failed`). Code mapping (G19):
`platform_bearer_required` (401) exits 6, `actor_required` (403) exits 7.

`realm-id explain needs_human` and `split_upload_not_allowed` answer from a small CLI-local table:
the vendored error reference is hash-gated and re-vendored only at a release tag.
