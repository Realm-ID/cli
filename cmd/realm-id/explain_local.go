package main

// cliLocalErrorRows are codes the CLI itself speaks (or the issuer answers) that the vendored
// error reference does not carry yet. The reference is hash-gated and re-vendored only at a
// release tag, so these ride beside it until it catches up; a code that later appears in the
// vendored file is shown from there too (explain lists every row).
func cliLocalErrorRows() []errorRow {
	const sec = "CLI (local; pending the next error-reference re-vendor)"
	return []errorRow{
		{Code: "needs_human", Section: sec,
			Meaning:  "[CLI exit 8] This verb needs a person's session. A key session (REALM_ID_API_KEY) or a workload session cannot run it; nothing was sent. The one exception is the platform_mgmt_api workload actor minting or revoking platform keys (R72).",
			WhatToDo: "Run `realm-id auth login` as the realm owner and repeat the command."},
		{Code: "split_upload_not_allowed", Section: sec,
			Meaning:  "[409] The realm's custody state does not accept an upload of a split key now (for example, during an overlap). The first upload is the realm owner's; a rotation or recovery goes through a lease.",
			WhatToDo: "Check `realm-id signing-keys status`; finish or cancel the overlap, then use `signing-keys rotate`."},
	}
}
