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
```

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
- **Where it talks** — the typed tree is the issuer's admin contract. With
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

Exit codes (for agents): `0` ok · `2` usage · `4` conflict · `5` not-found ·
`7` forbidden · `1` other.
