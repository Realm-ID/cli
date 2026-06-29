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
realm-id users list --tenant ten_123 --status active
realm-id users set-role --tenant ten_123 --uid usr_9 --field role:=\"owner\"
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
- **Where it talks** — the typed tree is the issuer's admin contract, so it runs
  **issuer-direct** (`auth.realmid.dev`). With `REALM_ID_API_KEY=rk_live_…` set it
  uses that platform key (ADR-062 §4 Service mode); otherwise it falls back to the
  `auth login` session bearer.
- **Collisions** — where a hierarchical API flattens to the same `resource verb`
  (e.g. platform- vs tenant-scoped `identity-providers list`), the broadest-scope
  variant wins; the narrower ones stay reachable via `realm-id api`.

`auth login` opens the approval link in your default browser (best-effort, only on
an interactive terminal) and prints it as a fallback; the link already carries the
one-time code, so you never type or match a code. Re-running `auth login` supersedes
any earlier run still waiting — the older poller stops on its next tick. Set
`REALM_ID_NO_BROWSER=1` to suppress the auto-open (headless/agent/CI runs).

Config lives at `~/.config/realm-id/config.json` (mode `0600`; it holds the session
bearer). Overrides: `REALM_ID_BFF`, `REALM_ID_ISSUER`, `REALM_ID_API_KEY`,
`REALM_ID_CONFIG`, `REALM_ID_NO_BROWSER`.

## Status

Shipped: `auth` (device flow), `config`, the generic `api` passthrough, `schema`,
and the **typed command tree** (platforms, tenants, users, invitations, api-keys,
roles, federation-bindings, origins, domains, identity-providers, audit-events, +
`admin …`) generated from the OpenAPI spec (ADR-062 §1). Destructive verbs
(delete / signing-key rotate / suspend / ownership transfer) are intentionally
absent pending machine-2FA (ADR-062 §5).

Exit codes (for agents): `0` ok · `2` usage · `4` conflict · `5` not-found ·
`7` forbidden · `1` other.
