# realm-id

The agent-first CLI for the [RealmID](https://realmid.dev) identity platform (ADR-062).

gcloud-shaped, JSON-emitting, and driveable by humans **and** AI agents. `auth login`
uses the OAuth 2.0 Device Authorization Grant (RFC 8628), so a terminal or agent
authenticates by opening a link — no embedded browser.

## Install

```bash
go install github.com/Realm-ID/cli/cmd/realm-id@latest
```

(Homebrew / prebuilt binaries are on the roadmap — see the project `TODO.md`.)

## Usage

```bash
realm-id auth login              # authenticate via a browser link (device flow)
realm-id auth whoami             # show the current session identity
realm-id auth logout             # revoke + clear local credentials

realm-id config set platform <ref>   # set the active platform handle
realm-id config list                 # show the active configuration

realm-id api GET /me             # authenticated request through the BFF (JSON)
realm-id version
```

Config lives at `~/.config/realm-id/config.json` (mode `0600`; it holds the session
bearer). Overrides: `REALM_ID_BFF`, `REALM_ID_ISSUER`, `REALM_ID_API_KEY`,
`REALM_ID_CONFIG`.

## Status

v1 core: `auth`, `config`, and a generic authenticated `api` passthrough. The full
typed command tree (tenants, users, platforms, …) is generated from the issuer
OpenAPI spec in a follow-up (ADR-062 §1). Destructive verbs (delete / rotate /
transfer) are intentionally absent pending machine-2FA (ADR-062 §5).

Exit codes (for agents): `0` ok · `2` usage · `4` conflict · `5` not-found ·
`7` forbidden · `1` other.
