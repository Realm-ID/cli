# BACKLOG — cli

> Roadmap, post-v1, designed-not-built, or explicitly deferred work. See
> [`TODO.md`](TODO.md) for open, actionable work.

- **Acting MCP server** (owner, 2026-10-27, R42). Deferred out of the split-signing delivery.
  When built, it is a thin wrapper over the CLI binary (`realm-id … --json`), so it inherits
  every CLI rule (R40 `needs_human`, R41 MFA prompts, capped keys). Rulings: root
  `plans/2026-10-27-split-signing-rulings-r19-r48.md` § R42.

- **Rewrite the CLI in TypeScript on `@realm-id/web-admin`** (owner, 2026-11-02: decided NOT NOW).
  The CLI is a console-tier client (the terminal twin of the RealmID UI, not of the BFF), so its
  natural shared library is web-admin, where the browser key dealer `dealSplitKey` already lives;
  it must not depend on the server SDK `sdk/go`. Today the only logic the two clients share is the
  split dealer (~150 lines), kept identical by the CLI testing against
  `sdk/testdata/split-vectors/vectors.json`, which is far cheaper than a rewrite. Costs of a
  rewrite: ~4,900 lines of Go + ~4,100 of tests redone; the install loses its single static binary
  (`install.sh`, Homebrew, `go install`) for a Node runtime or a much larger bundled binary, which
  matters most on CI runners (workload sessions); the CLI generates a verb for EVERY spec
  operation while web-admin covers only what the console uses; web-admin is built around a
  browser session, the CLI around device-flow / key / workload BFF sessions.
  **Revisit when** the CLI needs substantial console logic beyond the dealer (more client-side
  crypto, multi-step flows the console already implements): then the duplication grows and a TS
  CLI on web-admin starts to pay for itself.
