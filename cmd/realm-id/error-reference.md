# Error reference

Every SDK failure raises a single error type — `RealmError` (TS),
`*RealmError` (Go), `RealmException` (Java) — carrying:

- `code` — stable, machine-readable identifier (table below).
- `message` — human-readable diagnostic.
- `httpStatus` — set when the failure originated from an API response.
- `details` — server-supplied envelope siblings
  (`mfa_challenge_token`, `revocation_token`, `active_sessions`,
  `tenant_id`, …).
- `cause` — wrapped underlying error.

The wire form of the code is identical across languages:
`MFA_REQUIRED.wire() === "mfa_required"` in Java equals
`err.code === "mfa_required"` in TS equals
`err.Code == realmid.ErrCodeMFARequired` in Go.

## Verifier codes

These fire from `realm.verify(token)`.

| Code                  | When                                                                | What to do                                                                               |
|-----------------------|---------------------------------------------------------------------|------------------------------------------------------------------------------------------|
| `malformed`           | Token isn't 3 dot-separated parts, or header/payload aren't JSON.   | Reject the request with 401. Don't retry.                                                |
| `wrong_algorithm`     | Header `alg` is not `RS256`.                                        | Reject. A non-RS256 token did not come from RealmID.                                     |
| `bad_signature`       | Signature did not verify against the realm's JWKS.                  | Reject. May indicate token forgery or a network-corrupted token.                         |
| `wrong_issuer`        | `iss` claim does not start with `${baseUrl}/${realmId}`.            | Reject. Either misconfigured `baseUrl` or token from a different realm.                  |
| `wrong_audience`      | `aud` claim does not equal the realm's audience (auto or override). | Reject. Tokens minted for a different audience cannot replay against your service.       |
| `expired`             | `exp` is in the past beyond leeway.                                 | Return 401. Client should refresh and retry.                                             |
| `not_yet_valid`       | `nbf` is in the future beyond leeway.                               | Reject. Likely clock skew if seen often.                                                 |
| `unknown_kid`         | Token's `kid` header is not in the (refetched) JWKS.                | Reject. Indicates key rotation that hasn't completed, or a forged token.                 |
| `jwks_fetch_failed`   | JWKS HTTP fetch returned non-200 or transport failed.               | Treat as transient. The SDK does not retry; caller may.                                  |

## Auth-flow codes

These fire from `realm.auth.*` calls.

| Code                      | When                                                                                         | What to do                                                                                                |
|---------------------------|----------------------------------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------|
| `provider_token_invalid`  | Upstream IdP rejected the token (Firebase / Google).                                         | Surface to user. Often means the IdP session expired client-side.                                         |
| `mfa_required`            | Login or refresh succeeded as far as identity, but MFA step-up is required.                  | Read `details.mfa_challenge_token` and `details.methods`; prompt user for code; call `auth.mfaVerify`.    |
| `session_limit_reached`   | The user has too many active sessions for the realm's policy.                                | Read `details.revocation_token` + `details.active_sessions`; let user pick one to revoke; retry login.    |
| `tenant_required`         | `auth.token` was called without a `tenantId` and the user belongs to multiple.               | Surface a tenant picker; supply `tenantId` to `auth.token`.                                                |
| `tenant_invalid`          | `tenantId` is not a tenant the user belongs to in this realm.                                | Reject; the picker shouldn't have offered it.                                                              |
| `token_stale`             | (401, verifier-emitted, ADR-107) The token was minted before its subject's authority changed.  | Refresh ONCE and replay; never treat it as a sign-out (check it before any generic 401 handling).        |
| `user_suspended`          | (403; the issuer also sends 401 on the user-API-key exchange) The user's row in THIS org is suspended or deactivated (ADR-104 R-d). | Show "not allowed in this org". Do not sign the user out of other orgs; the Go, TS and Java BFF middleware relay it without tearing the session down. Breaking in go 0.66.0: it used to be `forbidden` + `details.server_code`. |
| `escort_suspended`        | (403 from the issuer, shown as 503 by the Go, TS and Java BFF middleware) Your backend's `/auth/*` escort account is suspended or deactivated (ADR-104 R-d). | A service problem, not the user's: alert operators. The session is untouched. |
| `tenant_disabled`         | (403) The organization this token is for is suspended or deactivated (ADR-049 amendment, 2026-10-16). Sent on bearer requests, on refresh and on a login that asks for that org. | Show "this organization is unavailable" and offer the other orgs. Do not sign the user out: the session is NOT ended, other orgs keep working, and the same token works again once the org is reactivated. Not the 410 `tenant_suspended` of the integration routes. |
| `account_suspended`       | The user's account is suspended.                                                             | Surface a "your account is suspended" message; do not retry.                                              |
| `account_deactivated`     | The user's account is deactivated.                                                           | Surface a "this account has been closed" message.                                                         |
| `realm_origin_mismatch`   | The request's `Origin` header disagrees with the body's `realm_id`.                          | Configuration error — either set `createRealm({ origin })` correctly or stop overriding per call.         |
| `bff_bearer_required`     | A `/auth/*` call reached the issuer without a platform token (ADR-088 — the BFF requirement is unconditional). | Call through the SDK's `realm.auth.*` (it attaches the platform token); never let a browser hit the issuer directly. |
| `missing_origin`          | Server requires an Origin (or body `realm_id`) and got neither.                              | The SDK auto-attaches Origin from `realm.info()`, so this should not fire under normal use.               |

## Sign-in, token and escort codes (issuer wire codes)

These are the codes the issuer itself puts on `/auth/*` responses (and on the escort headers a
backend sends with them). The SDK maps most of them onto the codes above; a few arrive verbatim
as `code`. HTTP status is in brackets.

| Code | When | What to do |
| --- | --- | --- |
| `bad_json` | [400] The request body is not valid JSON. | Fix the request; do not retry unchanged. |
| `internal` | [500] The issuer failed on its side; the body is deliberately generic. | Treat as transient. If it repeats, send support the time and request id. |
| `missing_grant_type` | [400] `POST /auth/login` had no `grant_type`. | Send `grant_type` (the old `method` field is gone). |
| `bad_grant_type` | [400] `grant_type` is not one the issuer supports. | Use one of the documented grants. |
| `missing_provider` | [400] `grant_type=provider_token` without `provider`. | Send the provider name alongside the token. |
| `unknown_method` | [400] The login method is not enabled for this realm (a provider with no configured verifier, or password sign-in switched off). | Do not offer that method; read the realm's discovery document to see what is on. |
| `bad_tenant_id` | [400] A provider login named a `tenant_id` that is not in this realm. | Drop it (realm-root sign-in needs none) or send a tenant of this realm. |
| `missing_api_key` | [400] An api-key grant had no `api_key`. | Send the key. |
| `missing_tenant_id` | [400] `POST /auth/token` refreshed a **user** session without `tenant_id`. Service and platform sessions need none. | Send the `tenant_id` of the org the session is for. |
| `missing_fields` | [400] MFA verify without `mfa_challenge_token` or `code`. | Send both. |
| `scope_not_supported` | [400] A `scope` was requested on a session class that mints no `scope` claim. | Omit `scope`. |
| `reserved_claim_key` | [400] A `custom_claims` key is a reserved JWT claim name. | Rename the key. |
| `password_too_short` / `password_too_long` | [400] A new password is under 12 or over 1024 bytes. | Ask for a new password within bounds. |
| `password_unchanged` | [400] The new password equals the current one. | Ask for a different password. |
| `password_change_not_required` | [400] A change was sent at sign-in but the password has no pending change. | Omit `new_password`; change it later through the password-change call. |
| `invalid_credentials` | [401] The password, api key, one-time code or provider token was wrong. Unknown identifier and wrong secret look identical on purpose. | Re-prompt. Do not tell the caller which part was wrong. |
| `invalid_audience` | [401] The provider token's `client_id` (or Firebase project) is not registered for this realm or tenant. | Register the client id in the realm's identity-provider settings. If it is registered for one organisation only, send that organisation's `tenant_id` on login. |
| `revoked_api_key` | [401] The api key was revoked. | Provision a new key. |
| `keyless_api_key` | [401] The api key is not bound to a bot user. | Re-provision the key. |
| `refresh_invalid` | [401] The refresh token is expired, revoked, or was replayed. Terminal. | Send the user through sign-in again; retrying cannot help. |
| `m2m_refresh_withdrawn` | [401] The session came from an api key or workload assertion and has no refresh token. | Mint again from the key or assertion; do not refresh. From the split-signing release (R79, `### Breaking`) this also covers a session started by a `user_api_key` sign-in. |
| `bff_bearer_invalid` | [401] The platform bearer on an `/auth/*` call has a bad signature or audience, or its session is revoked. | Fetch a fresh platform token. |
| `bff_bearer_expired` | [401] The platform bearer has expired. | Fetch a fresh platform token and retry. |
| `bff_bearer_realm_mismatch` | [401] The platform bearer was issued for a different realm. | Use the bearer for the realm you are calling. |
| `bff_bearer_invalid_tenant` | [401] The bearer's tenant is not this realm's admin tenant. | Use a platform token, not an org user's token. |
| `bff_bearer_invalid_role` | [401] The bearer is not a platform-API or owner token for this realm. | Use the platform key's token. |
| `x_user_token_invalid` | [401] `X-User-Token` has no subject. | Forward a token the issuer minted. |
| `x_user_token_realm_mismatch` | [401] `X-User-Token` belongs to a different realm than the bearer. | Pair tokens from the same realm. |
| `insufficient_scope` | [401] A revocation token was presented where it may not bulk-revoke sessions. | Use the user's access token. |
| `not_a_member` | [403] The user is not a member of the requested tenant. | Offer only tenants the user belongs to. |
| `no_permissions_in_org` | [403] A key's permission cap and the holder's role in the key's org have nothing in common. | Re-issue the key with a cap the holder's role covers. |
| `user_not_invited` | [403] Sign-in by someone with no membership anywhere, in a realm that does not allow open sign-up. | Ask an org owner for an invitation. |
| `signup_domain_not_allowed` | [403] The email domain is not allowed to sign up. | Use an address on an allowed domain. |
| `password_must_change` | [403] The password was correct but an administrator set it and it must be changed first. | Collect a new password and resend it as `new_password`. |
| `realm_not_found` | [404] The realm the bearer is for does not exist. | Check the realm id. |
| `contact_admin_required` | [409] The identifier is managed by the user's organization and is bound to a different provider. | Tell the user to contact their organization's admin. |
| `no_verified_email` | [409] A reset link was requested for a member with no verified email. | Have the member verify an address first. |
| `reset_token_invalid` | [400] The password-reset link is not valid. | Ask for a new link. |
| `reset_token_expired` | [410] The reset link has expired. | Ask for a new link. |
| `reset_token_used` | [410] The reset link was already used. | Ask for a new link if the password still needs resetting. |
| `reset_token_superseded` | [410] A newer link was sent, or the password changed since. | Use the newest link. |
| `realm_suspended` | [410] The realm is suspended or deactivated. | Surface a service-unavailable message; do not retry. |
| `mfa_registration_required` | [412] Sign-in is blocked until the user enrols an authenticator; the envelope carries a challenge token. | Run the first-login enrolment flow with `details.mfa_challenge_token`. |
| `first_login_pending_approval` | [412] First sign-in on a recently vacated contact awaits the tenant owner's approval. | Tell the user to wait for the owner; nothing to retry. |
| `mfa_too_many_fails` | [429] Too many wrong MFA codes for this challenge. | Back off; the user may need to restart sign-in. |
| `login_throttled` | [429] Too many attempts for this identifier; counted on the identifier as sent, so unknown and known ones throttle alike. | Wait before retrying. |
| `reset_throttled` | [429] Too many reset links were sent for this member in the last hour. | Wait before sending another. |

## One-time-code (OTP) codes (`/auth/otp/*`)

These come from issuing, viewing and verifying a one-time code. HTTP status is in brackets.

| Code | When | What to do |
| --- | --- | --- |
| `invalid_otp` | [401] The presented code does not match, or none was presented. | Ask the user to re-enter it. Repeated misses lock the code (`otp_locked`). |
| `otp_expired` | [410] The code's lifetime has passed. | Issue a new code. |
| `otp_locked` | [429] Too many wrong attempts against this code. | Stop; issue a new code after the user has waited. |
| `otp_not_found` | [404] No code with that id, or its plaintext is no longer available to display (the view route only shows a code for a short time). | Issue a new code. Do not treat it as proof the code was never issued. |
| `invalid_purpose` | [400] `purpose` is not lowercase letters, digits and underscores, starting with a letter, up to 64 characters. | Fix the request. |
| `invalid_subject_ref` | [400] `subject_ref` is empty or over 256 characters; for a `login` code it must be exactly `user:<id>`. | Fix the request. |
| `invalid_scope` | [400] The call has no tenant or no acting user to scope the code to. (A scope rename also uses it for a name that is not a valid RFC 6749 scope token.) | Call with a user-scoped, tenant-scoped token. |
| `unsupported_delivery_mode` | [400] `delivery_mode` is not `view_bff`, `email`, `sms` or `whatsapp`. | Use a supported mode, or omit it for `view_bff`. |
| `delivery_unavailable` | [503] The mode is valid but nothing can send it. For `sms` and `whatsapp` the body carries a `channel` field (`sms` or `whatsapp`; the SDKs put it in `details.channel`): the realm has not saved credentials for that channel, or the channel is marked `failing`. Not a registered SDK code: it reaches you as `details.server_code`. | Not the end user's fault. Offer the other channel if the realm lists one in discovery `phone_channels`; otherwise tell the realm owner to check the channel (`GET /platforms/{id}/messaging/{channel}` shows `status` and `status_reason`). |
| `phone_otp_rate_limited` | [429] This phone number was already sent the realm's `phone_otp` limit of codes (default 5 per 15 minutes, one counter for SMS and WhatsApp together) within the window. The `Retry-After` header gives the seconds to wait. | Show "try again in a few minutes" and wait; switching channel does not help. The realm owner can raise the limit with `phone_otp` in realm config, up to RealmID's ceiling (20 per hour; `window_seconds` may not exceed 3600, else `400 invalid_config_value`). |
| `sign_in_ip_rate_limited` | [429] Raised by the sign-in code send (`POST /auth/otp/send-login`; `auth.sendLoginCode`). The end user's IP (the `X-On-Behalf-Of-IP` you passed) has already requested the realm's `phone_otp.sign_in_per_ip_per_hour` sign-in codes this hour (default 10, range 1-60). An IPv4 address is counted per address, an IPv6 address per `/64`. Never raised when you pass no IP. The `Retry-After` header gives the seconds to wait. | Show "too many codes requested, try again later". Make sure you pass the END USER's IP, not your server's. |
| `invalid_on_behalf_of_ip` | [400] `X-On-Behalf-Of-IP` is present on the sign-in code send but is not a parseable IPv4 or IPv6 address. Nothing was counted or sent. | A caller bug: pass the end user's real address, or omit the header. |
| `method_not_enabled` | [403 on the sign-in code send; also [400] on the per-org domain-SSO routes (ADR-094)] The realm has one-time-code sign-in switched off (`otp_login_enabled = false`). A registered code in go/ts/java from the release that adds `sendLoginCode`; before that, the SDKs delivered it as `details.server_code` on the domain-SSO routes. | Do not offer the phone option. If you branched on `details.server_code == "method_not_enabled"`, branch on the code instead. |
| `no_delivery_address` | [400] `delivery_mode` is `email` and the code's subject has no email address on record (or `sms` / `whatsapp` and no phone number). Also returned when the provider says this ONE number cannot receive on the channel (invalid, opted out, not on WhatsApp; Twilio 21608, Meta 130497); the message then reads "this phone number cannot receive codes on <channel>". | Add an address first, or use another mode or channel. It never marks the channel `failing`. |

## Phone-code channel codes (`/platforms/{id}/messaging/{channel}`)

These come from saving, reading and deleting a realm's own SMS (Twilio) or WhatsApp (Meta) credentials. HTTP status is in brackets. All three need the realm permission `platform:config`.

| Code | When | What to do |
| --- | --- | --- |
| `invalid_channel_config` | [400] The saved body breaks a shape rule: neither or both Twilio auth forms (`auth_token` or `api_key_sid` + `api_key_secret`), neither or both sender forms (`from_number` in E.164 or `messaging_service_sid`), a field that belongs to the other provider, a field of the wrong type, or a secret (`auth_token`, `api_key_secret`, `access_token`) shorter than 20 characters. The message names the field. Nothing was stored. | Fix the named field and save again. |
| `channel_validation_failed` | [422] Twilio or Meta refused the credentials when RealmID checked them (no message is sent for this check). The message is the provider's reason, never a secret. Nothing was stored. | Read the reason, fix the credential in the provider's console, save again. |
| `channel_not_configured` | [404] The realm has no saved credentials for this channel. | For a first-time setup, show an empty form. Otherwise save the credentials with `PUT`. |

## API key codes

Platform API keys (`/platforms/{id}/api-keys`) and end-user API keys
(`/tenants/{tid}/users/{uid}/user-api-keys`), plus signing in with either. HTTP status is in brackets.

| Code | When | What to do |
| --- | --- | --- |
| `api_key_not_found` | [404] Revoking a platform API key id the realm does not have. | Re-list the keys and use a current id. |
| `api_key_cannot_mint_api_key` | [403] A caller that signed in with a platform API key tried to mint another platform API key. | Mint through workload identity federation or an owner login. |
| `invalid_user_for_api_key` | [400] The `user_id` given for a new platform API key is not a user of this realm, or its role is not `platform_api`. | Omit `user_id` to use the realm's default bot user, or pass a `platform_api` user. |
| `ambiguous_bot_user` | [400] No `user_id` was given and the realm has more than one `platform_api` user. | Pass `user_id` explicitly. |
| `invalid_ttl` | [400] A platform key request set both `ttl_seconds` and `non_expiring`. | Send only one. |
| `too_many_api_keys` | [409] The realm already holds the maximum of 2 active platform API keys. | Revoke one first. |
| `non_expiring_not_allowed` | [400] A non-expiring key was requested but not permitted: for platform keys the realm already holds one (at most one); for user keys the realm has not enabled `user_api_keys.allow_non_expiring`. | Give the key a TTL, or have the realm owner enable the setting. |
| `platform_api_key_login_disabled` | [403] The realm has turned platform API-key sign-in off. | Use workload identity federation. |
| `active_api_keys_exist` | [409] Turning off platform API-key sign-in while the realm still has active platform keys. | Revoke the active keys first, then retry. |
| `user_api_keys_disabled` | [412] End-user API keys are not enabled for this realm. | Have the realm owner enable them. |
| `uncapped_required` | [400] A user-key mint did not state `uncapped`. | Send `uncapped: true` (full authority of the holder) or `false` with a `permissions_cap`. |
| `uncapped_not_allowed` | [403] `uncapped: true` but the realm has not enabled `user_api_keys.allow_uncapped`. | Send a `permissions_cap`, or have the realm owner enable the setting. |
| `too_many_permissions` | [400] `permissions_cap` has more entries than the realm allows. | Send a shorter list. |
| `ttl_exceeds_max` | [400] `ttl_seconds` is above the realm's maximum for user keys. | Send a shorter TTL. |
| `too_many_active_keys` | [409] The user already holds the maximum number of active API keys. | Revoke one first. |
| `user_api_key_not_found` | [404] No such user key, or it belongs to a different user. | Re-list the user's keys. |
| `user_api_key_revoked` | [409] Updating a user key that is already revoked. | Mint a new key; a revoked key cannot be revived. |
| `org_not_in_key_scope` | [403] Signing in with a user key into an org outside the key's scope, or the key spans several orgs and none was chosen. | Pass a `tenant_id` the key covers. |

## Identity-provider codes (`/identity-providers`)

Configuring which sign-in providers a platform or organization offers. HTTP status is in brackets.

| Code | When | What to do |
| --- | --- | --- |
| `missing_platform_id` | [400] `platform_id` was not sent (query parameter on list, body field on create). | Send it. |
| `missing_id` | [400] The provider id path segment was empty. | Send a provider id. |
| `empty_patch` | [400] A PATCH carried none of the fields it accepts. (Also returned by the sign-in-source PATCH.) | Send at least one field to change. |
| `unsupported_provider` | [400] `provider` is not one of the supported providers. | Use a supported provider. |
| `unsupported_client_type` | [400] `client_type` is not supported. | Use one of `web`, `ios`, `android`, `desktop`, `other`. |
| `provider_exists` | [409] A row for this scope, provider and client type already exists. | Update the existing row instead. |
| `provider_not_found` | [404] No provider row with that id (or the id is not a valid UUID). | Re-list the providers. |
| `app_not_found` | [400] `app_id` does not name a known app (sign-in source). | Use an existing app id. |
| `app_platform_mismatch` | [400] `app_id` belongs to a different platform. | Use an app of this platform. |
| `app_kind_mismatch` | [400] `app_id` is a bot app, which has no provider registrations. | Bind the row to a human app. |
| `bad_platform` | [400] The public discovery `platform` query value is not `web`, `ios`, `android`, `desktop` or `other`. | Send a valid value or omit it (defaults to `web`). |
| `origin_tenant_mismatch` | [400] Public discovery was given both an origin and a `tenant_id`, and they resolve to different tenants. | Send only one, or matching ones. |

## Domain codes (`/domains/*` and `/platforms/{pid}/tenants/{tid}/domains`)

Claiming, verifying and binding domains for organizations, platforms and realm origins. HTTP status is in brackets.

| Code | When | What to do |
| --- | --- | --- |
| `missing_domain` | [400] The `domain` was empty. | Send a domain. |
| `unsupported_method` | [400] A domain claim `method` was not `dns_txt` or `html_file`. (The MFA self-enrol route also uses this code for a method other than `totp`.) | Use a supported method. |
| `tenant_not_found` | [404] The tenant in the path does not exist in that platform. | Check both ids. |
| `domain_not_found` | [404] Verifying a domain with no pending claim, or removing a domain that is not bound. | Claim the domain first, or re-list. |
| `domain_taken` | [409] The domain is already mapped to a different entity. | Use another domain, or contest it if you hold proof. |
| `already_bound` | [409] The domain is already bound to this tenant (nothing to do), or, creating a platform, already bound to a platform. | Treat as done, or pick another domain. |
| `domain_not_verified` | [412] Binding a domain as an origin, or creating a platform on it, before the domain is verified. | Verify the domain first. |
| `verification_failed` | [400] The proof check ran and failed; the message says why. | Fix the DNS record or file and verify again. |
| `contest_open` | [409] A contest for this domain is already open. | Wait for it to close. |
| `contest_cooldown` | [409] The current holder recently survived a contest and is in a cooldown window. | Try again after the window. |
| `contest_method_mismatch` | [409] A contest must use the same verification method as the current holder. | Verify using that method. |
| `origin_not_found` | [404] No such realm origin, or it is already detached. | Re-list origins. |
| `last_origin` | [409] Detaching the realm's only bound origin. | Bind another origin first. |
| `grant_exists` | [409] The organization already holds a grant for this domain. | Update the existing grant. |
| `grant_not_found` | [404] No domain grant with that id or domain. | Re-list the grants. |
| `invalid_status` | [400] A `status` filter value is not a known status (grants: use the documented statuses; users: `active`, `suspended`, `invited` or `deactivated`). | Fix the filter. |
| `not_a_proof_method` | [400] Verifying a grant whose evidence is not a proof method; there is nothing to verify. | Do not call verify for it. |

## Membership, invitation and ownership codes

Inviting people, accepting or leaving an organization, and transferring ownership
(`PUT /tenants/{id}/owner`). HTTP status is in brackets.

| Code | When | What to do |
| --- | --- | --- |
| `invalid_identifier` | [400] An invite identifier (or `new_owner_email`) is not an email address or E.164 phone number. | Fix the address. |
| `already_member` | [409] The identifier is already linked to an existing member of the organization. | Nothing to invite; manage the existing member. |
| `owner_not_invitable` | [400] An invite asked for the `owner` role. | Invite as another role, then use ownership transfer. |
| `invite_not_found` | [404] No such invitation in this organization. | Re-list invitations. |
| `invitation_address_immutable` | [400] Changing the contact of a member whose invitation is still pending. | Revoke the invitation and invite the corrected address. |
| `invitations_unavailable` | [501] The deployment has no invitation store configured for the accept or reject routes. | Not a request error; contact support. |
| `membership_not_found` | [404] The caller has no such membership or invitation. | Re-list the caller's memberships. |
| `not_invited` | [409] Rejecting a membership that is not a pending invitation. | To end an active membership, use leave. |
| `not_pending` | [409] There is no pending invitation to reject, or it is no longer pending. | Re-list invitations. |
| `already_left` | [409] The membership has already ended. | Nothing to do. |
| `owner_cannot_leave` | [409] The caller owns the organization and tried to leave it. | Transfer ownership first. |
| `owner_cannot_be_revoked` | [409] In a single-organization realm, choosing an organization would end the caller's membership in another one they own. | Transfer that organization's ownership first. |
| `single_tenant_not_required` | [409] The realm does not require single-organization membership, so there is nothing to choose. | Skip the choice step. |
| `single_tenant_membership` | [409] The realm allows one organization per user and the user belongs to another. | Have the user leave the other organization first. |
| `last_owner` | [409] Changing the role of, or deactivating, the organization's owner. | Transfer ownership first. |
| `use_transfer_owner` | [400] A role change asked for `owner`, or `outgoing_owner_role` was `owner`. | Use the ownership-transfer route; `outgoing_owner_role` must be a different role. |
| `missing_recipient` | [400] An ownership transfer named no recipient (`owner_user_id` or `new_owner_email`). | Name the new owner. |
| `recipient_contact_unproven` | [400] The address given as new owner is not a proven contact in this organization. | Name the recipient with `owner_user_id`, or have them verify the address first. |
| `recipient_not_member` | [400] The new owner is not a member of this organization. | Add them as a member first. |
| `recipient_not_active` | [400] The new owner is a member but not active. | Pick an active member. |
| `conflicting_outgoing_disposition` | [400] `leave_entirely` and `suspend_outgoing_owner` were both set. | Send only one. |
| `owner_transfer_requires_human` | [403] An owner-initiated transfer was attempted with a platform-scoped or tenant-less token. | Make the call with the current owner's own user session. |
| `tenant_deleted` | [409] Patching an organization that has been deleted. | A deleted organization cannot be revived this way. |

## Contact review codes (`/tenants/{id}/contact-verifications`, `contact-drift-reviews`, hand-back)

Organization admins reviewing contact changes, delinking a contact and handing an account back. HTTP status is in brackets.

| Code | When | What to do |
| --- | --- | --- |
| `contact_not_found` | [404] The contact does not belong to that user in this organization. Also (registered with the contact-proof routes, issuer spec 0.62.0): `/me/contacts/{id}/proof[/confirm]` on a contact that is not an active phone of the caller's own membership; an admin acting for the person, a foreign id and an unknown id are indistinguishable. | Re-list the user's contacts. |
| `user_not_found` | [404] The user does not exist in this organization. | Check the id. |
| `from_user_not_found` | [404] The `from_user_id` account does not exist in this organization. | Check the id. |
| `missing_from_user_id` | [400] Hand-back was called without `from_user_id`. | Send it. |
| `same_user` | [400] `from_user_id` equals the target user. | Name a different source account. |
| `target_not_deactivated` | [409] The hand-back target account is not deactivated. | Only a deactivated account can be handed back. |
| `owner_not_handbackable` | [409] The source account is the organization's owner. | Transfer ownership first. |
| `no_source_identity` | [400] The source account has no active email to hand back. | Hand-back needs a source with an active email. |
| `handback_insert_failed` | [409] The handed-back identity could not be planted (the message says why). | Resolve the clash it names and retry. |
| `drift_review_not_found` | [404] No such review in this organization. | Re-list the reviews. |
| `drift_review_not_pending` | [409] The review was already resolved. | Nothing to do. |
| `verification_not_found` | [404] No such contact verification in this organization. | Re-list the verifications. |
| `verification_not_pending` | [409] The verification was already resolved. | Nothing to do. |
| `verification_slot_taken` | [409] Approving would take a verification slot that is already used (the message says why). | Resolve the existing verification first. |
| `identifier_collision` | [409] Accepting a review, or setting a username, would reuse a value already in use in this organization. | Choose a different value. |

## Credential, realm-config, MFA and paging codes

Setting passwords and usernames, patching realm settings, TOTP self-service, and list paging.
HTTP status is in brackets.

| Code | When | What to do |
| --- | --- | --- |
| `current_password_required` | [400] Replacing an existing password without sending `current_password`. | Send the current password. |
| `reset_link_required` | [409] An admin tried to set a password for a member who has a verified email. | Send the member a password reset link instead. |
| `verified_email_required` | [409] The realm requires a verified email before a username or password is set, and the member has none. | Have the member verify an email first. |
| `invalid_username` | [400] The username is not 3-32 characters, starting and ending alphanumeric, with only `.`, `_`, `-` inside, not all digits and without `@`. | Choose a valid username. |
| `password_too_long` | [400] The password is over 1024 bytes. (A password under 12 bytes is `password_too_short`.) | Send a shorter password. |
| `invalid_config_value` | [400] A realm-settings value has the wrong type (`password_policy.require_verified_email` must be a boolean), or an organization `status` is not `active`, `suspended` or `deactivated`. | Fix the value. |
| `unknown_config_key` | [400] A realm-settings patch named a key that group does not have (`org_sessions` accepts only `mode`; `password_policy` only `require_verified_email`). | Remove the key. |
| `invalid_org_sessions_mode` | [400] `org_sessions.mode` is not `concurrent` or `exclusive`. | Use one of the two. |
| `missing_code` | [400] MFA step-up was called without `code`. | Send the TOTP code. |
| `not_enrolled` | [400] MFA step-up for a user who has not enrolled, or has not confirmed enrolment. | Enrol and confirm first. |
| `already_enrolled` | [409] Starting TOTP enrolment for a user who is already enrolled. | Reset the enrolment first. |
| `invalid_cursor` | [400] `cursor` is not one this server issued. | Omit it for the first page, or pass the previous `next_cursor` unchanged. |
| `invalid_limit` | [400] `limit` is not a whole number of at least 1. | Fix it, or omit it for the default of 50. |
| `session_not_found` | [404] No such session for this identity in the realm. | Re-list sessions. |

## Scope, permission, import and role-authoring codes

HTTP status is in brackets.

| Code | When | What to do |
| --- | --- | --- |
| `too_many_scopes` | [400] The `scope` on a sign-in request holds more entries than the realm allows. | Send fewer scopes. |
| `scope_too_long` | [400] A scope entry (on sign-in, or in a scope rename) is longer than the realm's maximum. | Use a shorter scope name. |
| `permission_cap_exceeded` | [403] The caller's API key has a `permissions_cap` that does not include the permission the call needs. This differs from a plain `forbidden`: the user may hold the permission, the key's cap excludes it. | Mint a key whose cap includes it. |
| `realmid_audience_immutable` | [400] A scope rename on a realm whose permission vocabulary is RealmID's own catalog. | Do not rename it. |
| `not_implemented` | [501] Scope rename is not available on this deployment. | Not a request error; contact support. |
| `platform_not_found` | [404] The platform id does not exist (or is not a valid UUID). | Check the id. |
| `empty_import` | [400] A bulk user import had no rows. | Send at least one row. |
| `too_many_rows` | [400] A bulk user import exceeded the per-request row cap (1000). | Split it into batches. |
| `role_authoring_retired` | [403] A partner realm tried to create or edit role templates. RealmID defines the role set. | Put product roles in your own system and send them as scopes; disable a role you do not want with `POST /platforms/{id}/roles/{roleId}/disable`. |

## Roles, service accounts and integration registry codes

HTTP status is in brackets.

| Code | When | What to do |
| --- | --- | --- |
| `unknown_role` | [400] The role is not registered in the realm's role catalog. (A bulk import reports this per row.) | Use a role from `GET /platforms/{id}/roles`. |
| `role_disabled` | [400] The role is disabled in the realm, or switched off for this organization. | Choose another role, or re-enable it. |
| `role_not_assignable_to_kind` | [400] The role cannot be held by this kind of principal (a human-only permission on a service account, or a role not open to that kind). | Pick a role that fits the principal. |
| `role_not_found` | [404] Disabling or enabling a role id the realm does not have. | Re-list the roles. |
| `role_protected` | [400] Disabling a protected role, which must stay on. | Leave it enabled. |
| `role_is_default` | [400] Disabling the realm's default invitation role. | Set another default first. |
| `last_active_role` | [400] Disabling would leave the realm with no active role besides `owner`. | Keep at least one other role active. |
| `unknown_permission` | [400] A permission string is empty or not in the permission catalog (also for `permissions_cap` on a RealmID-audience user key). | Use a permission from `GET /platforms/{id}/permissions`. |
| `starter_roles_retired` | [400] Creating a platform with `starter_roles`; the field is retired. | Drop it. `admin` is in every realm; to run without it, disable it. |
| `invalid_rename` | [400] A scope rename where `to` equals `from`. | Send different names. |
| `invalid_handle` | [400] A service-account login handle is not email-shaped (no `@`). | Use an email-shaped handle. |
| `handle_taken` | [409] The service-account handle is already in use in this organization. | Pick another handle. |
| `invalid_role` | [400] A service account may not hold `owner` or `platform_api`. | Choose another role. |
| `platform_bot_immutable` | [400] Modifying the platform API bot, which RealmID manages. | Rotate its credentials through the API-keys routes instead. |
| `service_account_not_found` | [404] No such service account in this organization. | Re-list service accounts. |
| `invalid_slug` | [400] An integration `slug` is not 2-48 lowercase letters, digits and single hyphens; or a platform slug is not 1-63 of lowercase a-z, 0-9, hyphen (no leading or trailing hyphen). | Fix the slug. |
| `reserved_slug` | [409] The platform slug is reserved by RealmID. | Choose another slug. |
| `display_name_required` | [400] An integration `display_name` is missing or blank. | Send one. |
| `integration_not_found` | [404] No such integration (in your realm, or at install time). | Check the id. |
| `invalid_integration_id` | [400] `integration_id` is not a UUID. | Send a UUID. |
| `integrations_unavailable` | [501] The deployment has no integration registry configured. | Not a request error; contact support. |
| `tenant_suspended` | [410] The organization is suspended or deactivated (also when a cross-realm mint targets such an org). | Reactivate it, or choose another. |
| `handle_collision` | [409] The new install's principal handle collided (very unlikely). | Retry. |
| `invalid_installation_id` | [400] A cross-realm mint's `installation_id` is not a UUID. | Send the installation's UUID. |
| `missing_source_org_id` | [400] A cross-realm mint did not send `source_org_id`. | Send it; it is the only attribution the target org gets. |

## Creating an organization (`POST /platforms/{pid}/tenants`)

HTTP status is in brackets.

| Code | When | What to do |
| --- | --- | --- |
| `base_realm_no_orgs` | [403] Creating an organization under the base realm. | Create it under a partner platform. |
| `missing_display_name` | [400] `display_name` is empty. | Send it. |
| `invalid_created_at` | [400] `created_at` is not an RFC 3339 timestamp (a bulk import reports this per row). | Fix the timestamp, or omit it. |
| `invalid_tenant_id` | [400] The caller-supplied `id` is not a UUID. | Send a UUID, or omit it. |
| `cross_realm_tenant_id` | [400] The supplied tenant id (or owner id) already exists in another realm. | Use a fresh id. |
| `invalid_signup_mode` | [400] `signup_mode` is not `closed`, `allowlist` or `open`. | Use one of the three. |
| `signup_mode_immutable_on_reconcile` | [409] Re-sending an existing organization (same id) with a different `signup_mode`. | Change it with `PATCH /tenants/{id}/config`. |
| `owner_required` | [400] `owner` is missing. | Send an owner. |
| `owner_missing_identifier` | [400] The owner has neither an email nor a phone. | Send one. |
| `owner_invalid_email` | [400] The owner email has no `@`. | Fix it. |
| `owner_invalid_phone` | [400] The owner phone is not canonical E.164 (`+`, non-zero country digit, 8-15 digits, no spaces or punctuation). | Fix it. |
| `owner_invalid_user_id` | [400] `owner.user_id` is not a UUID. | Send a UUID, or omit it. |
| `owner_incomplete_provider` | [400] Only one of `provider` and `provider_uid` was sent. | Send both or neither. |
| `owner_unknown_provider` | [400] `provider` is not one of google, microsoft, apple, facebook, firebase. | Use one of those. |
| `cross_tenant_owner_id` | [400] `owner.user_id` already exists in another organization. | Use a fresh id. |
| `owner_already_set` | [400] Re-sending an existing organization with a different owner; reconcile never re-seats ownership. | Use the ownership-transfer route. |

## Custom claims, product roles and sign-in source codes

HTTP status is in brackets.

| Code | When | What to do |
| --- | --- | --- |
| `invalid_custom_claims` | [400] `custom_claims` on a sign-in request cannot be serialised as JSON. | Send plain JSON values. |
| `custom_claims_too_large` | [400] `custom_claims` serialises to more than 4096 bytes. | Send fewer or smaller claims. |
| `too_many_custom_claim_keys` | [400] `access_token_custom_claim_keys` in the tenant config lists more than 32 keys. | List fewer keys. |
| `custom_claim_key_too_long` | [400] An `access_token_custom_claim_keys` entry is longer than 64 bytes. | Shorten the key. |
| `too_many_product_roles` | [400] `product_roles` on a sign-in request has more than 16 entries. | Send at most 16. |
| `product_role_too_long` | [400] A `product_roles` entry is longer than 64 bytes. | Shorten it. |
| `invalid_product_role` | [400] A `product_roles` entry is empty, not valid UTF-8, or has control characters. | Fix the entry. |
| `unsupported_type` | [400] A sign-in source `type` is not `web`, `android`, `ios`, `desktop` or `bot`. | Use one of those. |
| `method_violates_kind` | [400] `allowed_methods` does not fit the source type: a bot source offers only `otp`, a human source can never offer `otp`. | Fix `allowed_methods`. |
| `source_not_found` | [404] No such sign-in source (or the id is not a UUID). | Re-list the sources. |

## Workload identity federation codes

Signing in with a workload's OIDC token, and managing trust bindings
(`/platforms/{id}/federation-bindings`). HTTP status is in brackets.

| Code | When | What to do |
| --- | --- | --- |
| `federation_disabled` | [501 on sign-in, 404 on the binding routes] Workload identity federation is not enabled on this deployment. | Use another sign-in lane; contact support. |
| `missing_subject_token` | [400] A federated sign-in had no `subject_token`. | Send the workload's token. |
| `bad_subject_token_type` | [400] `subject_token_type` is set and is not the JWT token type. | Omit it, or send the JWT type. |
| `unknown_issuer` | [400] A binding's `issuer` is not a supported federation provider. | Use a supported provider. |
| `unconstrained_binding` | [400] `match_claims` is empty; it must constrain the issuer's mandatory claim. | Add the claim. |
| `missing_mandatory_claim` | [400] `match_claims` does not constrain the issuer's mandatory claim. | Add it, with a non-empty value. |
| `invalid_mapped_role` | [400] The role a binding maps to does not exist, is disabled, or cannot be held by a service principal (or the realm's role catalog cannot be resolved). | Map to an enabled role that service accounts may hold. |
| `binding_exists` | [409] An active binding with these `match_claims` already exists. | Use the existing one, or revoke it first. |
| `binding_not_found` | [404] No such binding. | Re-list the bindings. |

## Platform-token and authorization codes

HTTP status is in brackets.

| Code | When | What to do |
| --- | --- | --- |
| `wrong_scope` | [401] A route that needs a platform token was called with a token that is not a platform-class token. | Use the platform token (exchange the platform API key). |
| `realm_mismatch` | [403] The platform token's realm is not the realm in the path; or a platform token tried to mint or manage user API keys for a realm other than its own. | Call with the right realm's token. |
| `tenantless_principal` | [403] The credential carries no tenant, so it cannot be authorized for a tenant-scoped decision. | Use a credential bound to a tenant. |
| `role_owner_only` | [403] Seating a member at a role that confers administrative authority, by someone who is not the organization owner. | Ask the owner. |
| `scope_mismatch` | [400] Minting a platform API key with a `scope` that disagrees with the bound user's key class (`platform_api` gives `platform`, `owner` gives `service`). | Omit `scope`, or match it. |
| `no_default_bot_user` | [400] No `user_id` was given and the realm has no `platform_api` user. | Pass `user_id` explicitly. |
| `platform_mgmt_api_is_wif_only` | [400] Minting an API key for the Platform Management API identity, which is WIF-only. | Use workload identity federation. |

## Cross-realm integration codes (`realm.integrations.*`, ADR-082/083)

> **Contract change (ADR-101 D7, issuer `v0.113.0`).** Installing an
> integration now *states* a permissions list; the role-based install
> (`role_id` + a `["service"]`-typed role) is retired. The issuer no
> longer emits `role_not_service_typed`, `role_not_installable` or
> `role_unavailable` — they stay in the SDK code unions only for older
> servers — and the published SDK `install()` bodies still send
> `role_id`, which a current issuer refuses with `permissions_required`
> (send `{ integration_id, permissions }` directly until the SDKs
> re-release).

| Code | Meaning | What to do |
| --- | --- | --- |
| `slug_taken` | `register` — slug already used in the realm. | Pick a different slug. |
| `permissions_required` | `install` — the stated grant is empty (or the retired `role_id` body was sent). | Name at least one ADR-074 catalog permission the integration may exercise. |
| `permissions_exceed_grantor` | `install` — the list names permissions the installing owner could not grant. | Narrow the list to what the owner actually holds. |
| `install_grants_nothing` | **`mint`, not `install`** — the installation row states no permissions and can authorise nothing. | Re-install stating permissions. A typo'd grant is refused at install time (`unknown_permission`), so this means an install that predates the check or was emptied. |
| `integration_disabled` | `install` / `mintToken` — the source platform disabled the integration. | Ask the source to re-enable it. |
| `already_installed` | `install` — a live installation already exists for this org. | Uninstall the existing one first, or reuse it. |
| `installation_revoked` | `mintToken` — the target org uninstalled. | Stop minting; the edge is gone. |
| `key_class_mismatch` | `mintToken` — the api key is not a platform-class key. | Mint with the realm's `platform_api` key, not a service key or user token. |
| `installation_not_found` | `mintToken` — unknown installation id, **or** a platform key from a realm other than the integration's (no cross-realm existence oracle). | Verify the installation id and that you are minting with the source realm's key. |

## Unique contact kinds, invitation links and contact proof (issuer spec 0.62.0, ADR-117)

A platform declares which contact kinds are identifying (`contacts.unique_kinds`); these codes come
from changing that list, from the routes that depend on it, and from the invitation-link and
phone-proof flows. HTTP status is in brackets. The 409 bodies carry their offender in `details`.
(`contact_not_found` is in the contact review table above; `invalid_config_value` and
`identifier_collision` already existed and are not newly registered.)

| Code | When | What to do |
| --- | --- | --- |
| `unique_kinds_requires_password` | [400] Config PATCH would leave `contacts.unique_kinds` empty while `password_login_enabled` is false. | Turn password sign-in on in the same patch, or keep a kind. |
| `password_login_required` | [409] Config PATCH turns password sign-in off while the list is empty. | Keep a kind identifying first. |
| `unique_kind_lockout` | [409] The change would lock people out. `details`: `{kind, locked_out, mfa_locked_out}`. | Show both counts; resend with `confirm_lockout` (Go `ConfigUpdateOptions.ConfirmLockout`) only on an explicit human yes. |
| `unique_kind_lockout_check_failed` | [503] A count could not be taken (`-1` means "could not count", never "none"). Never escaped by `confirm_lockout`. | Retry later. |
| `unique_kind_duplicates` | [409] Adding a kind over duplicates inside one org. `details`: `{kind, conflicting_values}`. | Resolve the duplicates, then retry. |
| `email_keyed_signin_enabled` | [409] Removing `email` while email-keyed identity providers or SSO grants are enabled. `details`: `{identity_providers, sso_domains}`. | Disable or migrate those first. |
| `require_verified_email_conflict` | [409] `require_verified_email` with a non-identifying email, in either order. | Make email identifying, or drop the requirement. |
| `idp_requires_identifying_email` | [409] `POST/PATCH /identity-providers`: an email-keyed provider on a platform where email is not identifying. | Make email identifying, or do not enable the provider. |
| `idp_requires_identifying_phone` | [409] `firebase` provider when phone is not identifying either. | Make phone identifying, or do not enable the provider. |
| `domain_sso_requires_unique_email` | [409] Domain SSO (ADR-094) routes on a platform where email is not identifying. | Make email identifying first. |
| `invite_contact_not_identifying` | [400] An invitation (also org-create owner seed, bulk-import invite row) to a contact kind that is not identifying. | Invite by an identifying kind. |
| `recipient_email_not_identifying` | [400] Ownership transfer naming an email recipient while email is not identifying. | Name the recipient by an identifying kind. |
| `invalid_invitation_token` | [400] Inspect/accept: unknown, malformed, used, expired, superseded by a resend, withdrawn, or the membership is no longer invited. ONE code, no oracle. | Ask the inviter for a new link. |
| `contact_already_verified` | [409] `/me/contacts/{id}/proof`: the phone is already proven; nothing was sent. | Nothing to do. |
| `assertion_replayed` | [409] `POST /auth/login` (token-exchange grant): the workload assertion was already used (two instances fetched the same GCP ID token in the same second). Transient, not a credential rejection. The SDK retries it itself (1 s, then 2 s, re-fetching the credential, 3 attempts in all) and surfaces it only when all three are replayed. | Nothing for a normal start-up; if it surfaces, restart the instance. |
| `contact_changed` | [409] `/me/contacts/{id}/proof/confirm`: the contact was released or replaced after the send; the code is spent. | Start a fresh proof for the current contact. |
| `reset_email_not_identifying` | [409] `POST /tenants/{tid}/users/{uid}/password-reset`: no identifying verified email. | Use another recovery path. |

## Split signing, install pass and idempotency codes (issuer spec 0.65.0, UNRELEASED)

> **Added in spec 0.65.0 (unreleased).** These codes are in `issuer/docs/swagger.yaml` 0.65.0 and
> reach partners only with the single split-signing release (R62); until then no issuer answers
> them. HTTP status is in brackets. Background: `docs/partner-integration-guide.md` §6.8 and §6.9,
> and issuer ADR-068 / ADR-120. Rulings are cited by number (`plans/2026-10-27-split-signing-*.md`).

### Token and actor codes (R60, R61, R62, R78)

| Code | When | What to do |
| --- | --- | --- |
| `platform_bearer_required` | [401] The `Authorization` bearer is not a platform token (it is a user, key, service-account or integration token) (R60). The platform token always rides in `Authorization`; a person's token goes in `X-User-Token`. Exempt: `platform_api_key` and the WIF mint on `/auth/token`, and public routes. | Send your platform token in `Authorization`; the user's token goes in `X-User-Token`. The CLI maps it to exit 6. |
| `x_user_token_required` | [401] RETIRED AT RELEASE (R78). Today's issuer still answers it when `X-On-Behalf-Of-User` is sent with no `X-User-Token`. After the release the same call answers `403 actor_required`. | Send the person's token in `X-User-Token`. Treat a match as `actor_required` once the release is out. |
| `actor_required` | [403] The call carried a platform token and no `X-User-Token`, on an operation that is not on the no-actor list (R61). Replaces the retired `x_user_token_required` (R78): one code for "an actor is needed and there is none". | This action needs a user or service account. Send its token in `X-User-Token`. The SDKs raise a typed error (Go `ErrActorRequired`, ts `ActorRequiredError`, java `ActorRequiredException`); the CLI maps it to exit 7. |
| `login_grant_removed` | [400] A bot grant (`api_key`, `user_api_key`, `platform_api_key`, token exchange) was sent to `POST /auth/login` (R62). | Bot sign-ins moved to `/auth/token`. Upgrade the RealmID SDK. |

> `401 x_user_token_required` is retired (R78); no issuer answers it after the release. Treat any
> remaining match on it as `actor_required`. Until then it is still live: see its row above.

### Which endpoint, and what a realm does (`/auth/token`, `/auth/token/partial`, `/auth/login`)

| Code | When | What to do |
| --- | --- | --- |
| `split_signing_required` | [409] A mint or login on a SPLIT realm that would need a whole signature: `/auth/token` (and the people and bot grants of `/auth/login`) answer it for every grant except `platform_api_key` (R49) and a platform-token WIF exchange (R53). Added in spec 0.65.0 (unreleased). | Send the call to `POST /auth/token/partial` and finish the half-signed answer with your half. The SDK's `split: true` option does this. |
| `realm_not_split` | [409] `POST /auth/token/partial` on a realm that uses normal custody. Added in spec 0.65.0 (unreleased). | Use `POST /auth/token`. A configuration mismatch, not a transient fault. |
| `platform_grant_not_partial` | [400] `POST /auth/token/partial` with `grant_type=platform_api_key` (R49), or a token exchange that yields a PLATFORM token (R53). The platform key is never split (R45). The grant check runs first, so this wins over `realm_not_split` on a normal realm. Added in spec 0.65.0 (unreleased). | Send the platform grant to `POST /auth/token`, which serves it as a finished `Bearer` token on every realm. |
| `platform_key_not_a_user_key` | [400] `grant_type=user_api_key` presented an `rk_…` platform key (R24), checked before any lookup. Added in spec 0.65.0 (unreleased). | Use a `uk_live_…` user key, or `grant_type=platform_api_key` for a platform key. |
| `signing_key_unavailable` | [409] `/auth/token/partial`: RealmID cannot use its half of the split key (R39). Added in spec 0.65.0 (unreleased). | The partner SDK runs an emergency rotation (below). Do not retry in a loop. |

### Install pass and integration endpoint (ADR-082/083, R34, R47)

| Code | When | What to do |
| --- | --- | --- |
| `integration_grant_requires_install_pass` | [400] The removed direct `integration_installation` grant on `POST /auth/login`, authenticated by the source's `rk_live_` key (R34, R48). Added in spec 0.65.0 (unreleased). | Mint an install pass at `POST /integrations/installations/{install_id}/pass` and present it at the TARGET's token endpoint. |
| `missing_install_pass` | [400] `grant_type=integration_installation` on `/auth/token` or `/auth/token/partial` without `install_pass`. Added in spec 0.65.0 (unreleased). | Send the `ip_…` pass. |
| `install_pass_invalid` | [401] The `install_pass` is unknown, expired (5 minutes) or already used. A pass is a single exchange (R47). Added in spec 0.65.0 (unreleased). | Mint a fresh pass. Never retry a spent one. |
| `integration_endpoint_missing` | [409] An integration install into an org whose platform has not registered an `integration_token_endpoint` (R47). Added in spec 0.65.0 (unreleased). | The target platform registers its endpoint first. The base realm registers none, so nothing installs into it. |
| `missing_source_org_id` | [400] `POST /integrations/installations/{install_id}/pass` without `source_org_id`, the acting org of the source platform. It is the only attribution the target org gets for the acting call (R54b). Added in spec 0.65.0 (unreleased). | Send `source_org_id` in the request body. |
| `invalid_installation_id` | [400] The `install_id` on the install-pass mint is malformed. Added in spec 0.65.0 (unreleased). | Use the id returned when the integration was installed. |
| `invalid_integration_endpoint` | [400] The registered `integration_token_endpoint` is not an https URL. Added in spec 0.65.0 (unreleased). | Register an https URL. |
| `integration_endpoint_unverified` | [422] The registration test-call to the endpoint failed; the message names what the call got back. Added in spec 0.65.0 (unreleased). | Fix the handler (the SDK's ready-made handler answers the canary), then register again. |

### Split-key custody, handshake and rotation (`/platforms/{id}/signing-keys/split/...`)

| Code | When | What to do |
| --- | --- | --- |
| `split_key_size_unsupported` | [400] Upload of a key that is not RSA-2048. Added in spec 0.65.0 (unreleased). | Generate an RSA-2048 key. |
| `split_kid_in_use` | [409] Upload with a `kid` the realm already has. The caller chooses the kid. Added in spec 0.65.0 (unreleased). | Choose an unused `kid`. |
| `rotation_lease_held` | [409] Another holder has the 5-minute rotation lease (one holder at a time). Added in spec 0.65.0 (unreleased). | Wait for the lease to end or the other pod to finish; do not rotate from two pods. |
| `emergency_rotation_cap_reached` | [429] Emergency rotations are counted per realm per UTC day; this is past the cap (R39). Added in spec 0.65.0 (unreleased). | Stop and alert a human; the cap exists to bound a runaway loop. |
| `split_key_not_found` | [404] A handshake or challenge for a split key the realm does not have. Added in spec 0.65.0 (unreleased). | Upload the key first. |
| `handshake_failed` | [401] The completed signature over the challenge did not verify. A failure stops and alerts, it never loops (G16). Added in spec 0.65.0 (unreleased). | Check the share and its version; fix before retrying. The key does not become active. |
| `not_in_overlap` | [409] Extend or cancel while the key is not in the overlap window (R36). Added in spec 0.65.0 (unreleased). | Read the key's status first. |
| `downgrade_already_pending` | [409] Start a return to normal custody while one is under way. Added in spec 0.65.0 (unreleased). | Wait for it or cancel it. |
| `no_pending_downgrade` | [404] Cancel with no downgrade pending. Added in spec 0.65.0 (unreleased). | Nothing to cancel. |
| `not_available_to_api_keys` | [403] A downgrade call made with an API key. Only a human realm-owner session may start one (R55: API keys are refused the never-via-key operations). Added in spec 0.65.0 (unreleased). | Use an owner session. |

### Idempotency-Key (G11; parameter `IdempotencyKey` on 14 mutating operations)

| Code | When | What to do |
| --- | --- | --- |
| `idempotency_key_reused` | [422] The same `Idempotency-Key` was sent with a different body. Added in spec 0.65.0 (unreleased). | A key names ONE request. Generate a new key for a different body. |
| `idempotency_in_progress` | [409] A duplicate arrived while the first request with that key is still running. Added in spec 0.65.0 (unreleased). | Wait and retry with the SAME key and body; you then get the stored response. |

Swagger 0.65.0 lists these two only in the parameter description, not on the operations'
responses (critic finding #6, unresolved at the issuer). A request without the header behaves as before.

## Management / generic codes

These fire from any HTTP-backed call.

| Code            | When                                                                  | What to do                                                                                              |
|-----------------|-----------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------|
| `unauthorized`  | Server returned 401. API key revoked, platform token tampered, etc.   | Re-check credentials. The SDK will not retry.                                                            |
| `forbidden`     | Server returned 403. Role does not permit the operation.              | Surface a permission error. Check that the API key's role covers the call.                              |
| `not_found`     | Server returned 404.                                                  | Treat as resource-not-present. Useful for `realm.tenants.get(unknownId)`.                                |
| `conflict`      | Server returned 409. Optimistic-concurrency or unique-constraint violation. | Re-fetch and retry, or surface to user (e.g. duplicate tenant name).                                |
| `rate_limited`  | Server returned 429.                                                  | Read `Retry-After` from the error's `retryAfter` field (Go `RetryAfter`, ts `retryAfterSeconds`, Java `getRetryAfter()`). Back off and retry. |
| `bad_request`   | Server returned 400 / 412 with a non-mfa code, or the SDK rejected your input shape. | Inspect `details` for field-level info; fix the request.                                       |
| `network`       | Transport failure — DNS, TCP, TLS handshake, timeout.                 | Treat as transient. The SDK does not retry; caller policy varies.                                       |
| `server_error`  | Server returned 5xx, or returned an unexpected wire shape.            | Treat as transient. If repeating, capture the request id (in `details.request_id` if present) for support. |

## Working with envelope siblings

The 412 envelopes the server emits carry siblings alongside the
`error.code`:

```json
{
  "error": {
    "code": "mfa_required",
    "message": "MFA required for this resource"
  },
  "mfa_challenge_token": "ch_...",
  "methods": ["totp"]
}
```

The SDK lifts those siblings into `error.details`:

```ts
try {
  await realm.auth.login({ method: "firebase", providerToken: t });
} catch (e) {
  if (e instanceof RealmError && e.code === "mfa_required") {
    const ch = e.details?.mfa_challenge_token;
    const methods = e.details?.methods;
    // Prompt user; call realm.auth.mfaVerify({ challengeToken: ch, code, method })
  }
}
```

```go
var rerr *realmid.RealmError
if errors.As(err, &rerr) && rerr.Code == realmid.ErrCodeMFARequired {
    ch := rerr.Details["mfa_challenge_token"].(string)
    // ...
}
```

```java
try {
    realm.auth().login(req);
} catch (RealmException e) {
    if (e.getCode() == ErrorCode.MFA_REQUIRED) {
        String ch = (String) e.getDetails().get("mfa_challenge_token");
        // ...
    }
}
```

You never have to re-fetch context: every sibling the server emits is
already on the error object.
