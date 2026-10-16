# IM API v1 — initial implementation contract

This file coordinates the initial frontend, backend and deployment. JSON fields use camelCase, IDs are strings, timestamps are RFC3339 UTC. Responses are JSON objects (no success envelope). Errors use `{ "error": { "code": "forbidden", "message": "..." } }` with appropriate HTTP status. No Agent execution model appears in this API.

## Identity and session

- `POST /api/v1/auth/register` body `{email, password, displayName, inviteCode}` -> 201 `{user, csrfToken}` and login cookie. Registration is strictly invitation-only. When no accounts exist, only `BOOTSTRAP_INVITE_CODE` is accepted; its default is `teamie-bootstrap` if unset. That first account atomically becomes platform admin. After the first account exists, the bootstrap code is invalid for registration; only unused, unexpired, unrevoked admin-generated registration codes are accepted. Each ordinary code is consumed by exactly one successfully created user in the same transaction. Failed registration must not consume it. Concurrent requests must not create two bootstrap admins or spend one ordinary code twice. Password minimum 12 characters. Email uniqueness is case-insensitive.
- `GET /api/v1/auth/registration` -> `{mode:"invite",bootstrapRequired:boolean}`. Public, no-store, never returns any configured or generated invitation secret.
- `POST /api/v1/auth/login` body `{email,password}` -> `{user,csrfToken}` and cookie.
- `POST /api/v1/auth/password` body `{currentPassword,newPassword}` -> 204 without `Set-Cookie`. Only the current authenticated human account can change its own password, with the normal Origin and CSRF checks. The current password must match; the new password must differ, including under bcrypt comparison, and follow the registration limit of at least 12 Unicode code points and at most 72 UTF-8 bytes. Password replacement and revocation of every existing login session for that account commit atomically. A login that already checked the previous hash must not create a new session after the change. The revoked cookie can no longer authenticate; the browser clears its local login, CSRF token and data, then asks the user to sign in again. Omitting a cookie write prevents a delayed password response from deleting a newer login cookie. Incorrect current password returns 400 `invalid_current_password` and keeps the session; an invalid new password returns 400 `invalid_password`, an equivalent password returns 400 `password_unchanged`. Invalid or revoked sessions return 401. Rate limits apply. Passwords, hashes and session material never appear in response bodies or logs.
- Registration and password settings ask the user to confirm the new password in the browser. A mismatch is rejected before submitting; confirmation is a form check and is not sent as another credential field. Registration keeps its existing invitation-only API and atomic single-use invite consumption.
- `GET /api/v1/auth/session` -> `{user,csrfToken}` or 401.
- `POST /api/v1/auth/logout` -> 204, revokes the stored session.
- `PATCH /api/v1/auth/profile` body `{displayName}` -> `{user}`.
- User: `{id,email,displayName,kind:"human",platformRole:"admin"|"user",createdAt}`. Reserve `kind:"agent"` in contracts only. Server derives sender ID and platform role from persisted identity; neither is accepted from registration input. Platform admin is separate from workspace owner and receives no implicit access to other workspaces' messages.
- Opaque random `teamie_session` cookie, HttpOnly, SameSite=Lax, Secure on production HTTPS. Writes after login require `X-CSRF-Token`; reject untrusted Origin. Login/register also reject untrusted Origin. WebSocket requires matching Origin and current session.

## Platform registration invitations

- Only authenticated `platformRole=admin` may call these Identity-owned APIs. They use the same CSRF/Origin enforcement as other writes.
- `GET /api/v1/auth/admin/invites` -> `{invites:[{id,createdAt,expiresAt,usedAt?,revokedAt?}]}`. Do not return raw codes after creation or password/token material. A consumed user's ID may be included as `usedBy` for the administrator.
- `POST /api/v1/auth/admin/invites` body `{}` -> 201 `{invite:{id,code,createdAt,expiresAt},url}`. Codes are cryptographically random, single-use, expire after seven days, and stored only as a hash. `url` is the frontend-relative `/login?mode=register&inviteCode=<code>`. Raw code is shown only in this response.
- `DELETE /api/v1/auth/admin/invites/:id` -> 204; revoke an unused code (idempotent). A used code stays used and can never become reusable. Every invitation is consumed atomically with account/session creation. Invalid/expired/revoked/used codes return a consistent invalid-invitation error.
- Frontend route `/app/admin` provides generation, one-time code/link copy, status list and revoke for platform administrators. The login registration form accepts `inviteCode` from the URL and explains initialization when `bootstrapRequired=true`.
- `auth.public_signup` remains an unsupported false capability in this release: setting it true in a host file cannot bypass invitations.

## Chat bootstrap and workspaces

- `GET /api/v1/bootstrap` -> `{user,workspaces,conversations,capabilities,flagsRevision,syncCursor}`. `syncCursor` is informational; clients perform initial `/sync?cursor=0` and reconcile entity versions before adopting a new cursor, so bootstrap cannot introduce a message-loss window.
- Workspace: `{id,name,createdBy,createdAt}`.
- `POST /api/v1/workspaces` body `{name}` -> `{workspace,conversation}`. Creates membership and a default `general` channel atomically.
- `GET /api/v1/workspaces/:id/members` -> `{members:[{userId,displayName,role,kind}]}`. `role` is owner/member. Profiles may be resolved through Identity RPC.
- `POST /api/v1/workspaces/:id/invites` -> `{code,expiresAt}`. Owner only; return a frontend-relative `/join/<code>` link if useful.
- `GET /api/v1/invites/:code` -> `{workspace:{id,name},expiresAt}`. Public metadata only.
- `POST /api/v1/invites/:code/accept` -> `{workspace,conversations}`. Authenticated and idempotent membership admission.
- `DELETE /api/v1/workspaces/:id/members/:userId` -> 204. Owner only, cannot remove owner; removal revokes sends, reads and live delivery.

## Conversations and messages

- Conversation: `{id,workspaceId,type:"channel"|"direct"|"group",name,memberIds,lastSeq,readSeq,updatedAt}`. All initial conversations belong to a workspace; direct/group membership is explicit. Workspace channels are accessible to current workspace members.
- `GET /api/v1/workspaces/:id/conversations` -> `{conversations}`.
- `POST /api/v1/workspaces/:id/conversations` body `{type,name?,memberIds?}` -> `{conversation}`. Owner creates channels; current members create direct/group when capability permits. Direct chat is unique per workspace+unordered participant pair; server includes current user.
- `GET /api/v1/conversations/:id/messages?beforeSeq=<n>&limit=50` -> `{messages,hasMore}` in ascending seq order. Omit beforeSeq for latest page. Optional `q` performs literal, case-insensitive keyword search across the authorized conversation before pagination (maximum 200 characters); retracted bodies never match. `beforeSeq` pages matching results. Optional `aroundSeq` returns a sequence neighborhood centered on a referenced message and cannot be combined with `beforeSeq`. These generic history parameters apply equally to humans and authenticated participants.
- `POST /api/v1/conversations/:id/messages` body `{clientMessageId,body,replyToId?}` -> `{message}` (201 first write / 200 retry). Text max 8000 characters. Uniqueness `(senderId,clientMessageId)`. Conflicting retry body/conversation returns 409. Reply target must belong to same conversation.
- Message: `{id,conversationId,senderId,senderName,senderKind,clientMessageId,seq,body,replyToId?,createdAt,editedAt?,retractedAt?}`. `seq` is immutable message order. Edits/retracts use independently ordered sync events. Retracted bodies are omitted/empty for all reads.
- `PATCH /api/v1/conversations/:id/messages/:messageId` body `{body}` -> `{message}`. Author only, feature gated.
- `DELETE /api/v1/conversations/:id/messages/:messageId` -> `{message}` tombstone. Author only, feature gated.
- `POST /api/v1/conversations/:id/read` body `{seq}` -> `{readSeq}`. Monotonic, bounded by conversation lastSeq.
- Mentions use ordinary `@displayName` text initially; no notification/permission semantics are inferred from free text.
- Attachments, reactions, threads and search remain false capabilities in this release; the UI must not offer unsupported actions. Persisted attachment capability boundary is documented for the next release.

## Durable sync and WebSocket

- `GET /api/v1/sync?cursor=<opaque-or-decimal>&limit=100` -> `{events,cursor,hasMore}`. Empty/0 cursor starts the user's durable event stream. Stream is per-user; membership is rechecked before returning protected payloads. Cursor must advance past filtered events as well. Invalid/future cursor returns a defined error rather than silently skipping data.
- Event: `{id,type,conversationId?,workspaceId?,message?,conversation?,readSeq?,createdAt}`. `id` is the durable sync cursor. Types: `message.created`, `message.updated`, `message.retracted`, `conversation.created`, `membership.changed`, `read.updated`. A membership removal may invalidate cached entities; clients clear/refetch bootstrap.
- `GET /api/v1/ws` upgrades to WebSocket. Server emits JSON `{type:"ready"}` then `{type:"sync.required"}` as a hint. Clients use HTTP sync as the durable truth; hints can duplicate/coalesce and are never the only message payload. Reconnect always runs HTTP sync from the last applied cursor. Backoff with jitter, heartbeat and bounded write buffers.
- Client ephemeral messages `{type:"typing",conversationId,isTyping:true|false}`; server messages `{type:"typing",conversationId,userId,displayName,isTyping:true|false}` when supported. Typing uses the ordinary authenticated participant's current display name; it exposes no execution details. A positive hint expires five seconds after receipt unless refreshed; clients remove expired entries on their one-second cleanup tick. Stop hints clear immediately. Presence is optional and capability gated; it never controls message access.
- `POST /api/v1/conversations/:id/typing` body `{isTyping:boolean}` -> 204 is the equivalent generic HTTP signal, owned by Realtime. It accepts the normal human cookie/Origin/CSRF flow or an authenticated service principal bearer token plus service key, with the existing prohibition on browser bearer impersonation. The server derives sender ID/name, checks current conversation membership and `im.typing.publish` in both Realtime and Chat, and rechecks sender/recipient membership before delivery. Unknown fields, including sender IDs and runtime metadata, are rejected. Repeated positive hints are coalesced within 300ms per participant/conversation; stop hints are never throttled and a successful stop allows an immediate fresh start. Signals for a participant/conversation are published serially, so an earlier in-flight start cannot overtake a successfully published stop.
- Typing is ephemeral: no message, sync event, audit row, Session or activity resource is created. Redis loss may drop hints; failed clears or a publisher crash converge through the same five-second client TTL. Turning the write flag off rejects new signals, and existing ephemeral hints expire normally.
- Session revocation must disconnect or cause periodic revalidation; every protected sync/read/write must revalidate current identity/membership.

## Feature capabilities

`capabilities` is a map of booleans: `auth.public_signup`, `im.dm.create`, `im.group.create`, `im.channel.create`, `im.message.edit`, `im.message.retract`, `im.typing.publish`, `im.presence.publish`, `im.attachments.upload`, `im.reactions.create`, `im.threads.create`. Capability=true requires both implemented functionality and an enabled backend flag plus user permission. Persisted data is still returned if its write flag is turned off.

## Operational contract

Identity: HTTP `:8081`, Kitex RPC `:8881`; Chat: HTTP `:8082`, Kitex RPC `:8882`; Realtime: HTTP/WS `:8083`. Each exposes `/healthz` (liveness), `/readyz` (dependencies), `/version` with build SHA/version. Use env `HTTP_ADDR`, `RPC_ADDR`, `IDENTITY_RPC_ADDR`, `CHAT_RPC_ADDR`, `DATABASE_URL`, `REDIS_ADDR`, `APP_ORIGIN`, `COOKIE_SECURE`, `BOOTSTRAP_INVITE_CODE` (optional, default `teamie-bootstrap`), `FEATURE_FLAGS_FILE`. `REGISTRATION_CODE` is removed from the new pre-release design. Runtime secrets may use env backed by protected host config; never bake them into images. Each service applies its own embedded, forward-only idempotent migrations before readiness. Release scripts serialize migration and rollout.

Caddy routes `/api/v1/auth/*` to Identity, `/api/v1/ws` and `/api/v1/conversations/*/typing` to Realtime, other `/api/*` to Chat, and remaining requests to the static web container. Frontend uses same-origin relative URLs. Build context for backend Dockerfile is repository root, build arg `SERVICE=identity|chat|realtime`; executable packages are `./services/<service>/cmd`. Web build context is repository root, Dockerfile `apps/web/Dockerfile`.

Edge configuration requires `SITE_ADDRESS` equal to `APP_ORIGIN` and `TLS_SERVER_NAME` equal to that origin's hostname (without scheme/port). The latter configures Caddy `default_sni` for IP clients that omit SNI. It does not change the browser Origin or authorize another hostname.
