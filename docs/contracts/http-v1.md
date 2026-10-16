# IM API v1 — initial implementation contract

This file coordinates the initial frontend, backend and deployment. JSON fields use camelCase, IDs are strings, timestamps are RFC3339 UTC. Responses are JSON objects (no success envelope). Errors use `{ "error": { "code": "forbidden", "message": "..." } }` with appropriate HTTP status. No Agent execution model appears in this API.

## Identity and session

- `POST /api/v1/auth/register` body `{email, password, displayName, inviteCode}` -> 201 `{user, csrfToken}` and login cookie. Registration is invitation-only by default; the first release uses a deployment registration code, separate from workspace invitation codes. `auth.public_signup` may enable public registration. Password minimum 12 characters. Email uniqueness is case-insensitive.
- `POST /api/v1/auth/login` body `{email,password}` -> `{user,csrfToken}` and cookie.
- `GET /api/v1/auth/session` -> `{user,csrfToken}` or 401.
- `POST /api/v1/auth/logout` -> 204, revokes the stored session.
- `PATCH /api/v1/auth/profile` body `{displayName}` -> `{user}`.
- User: `{id,email,displayName,kind:"human",createdAt}`. Reserve `kind:"agent"` in contracts only. Server derives the sender ID from a validated session.
- Opaque random `teamie_session` cookie, HttpOnly, SameSite=Lax, Secure on production HTTPS. Writes after login require `X-CSRF-Token`; reject untrusted Origin. Login/register also reject untrusted Origin. WebSocket requires matching Origin and current session.

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
- `GET /api/v1/conversations/:id/messages?beforeSeq=<n>&limit=50` -> `{messages,hasMore}` in ascending seq order. Omit beforeSeq for latest page.
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
- Client ephemeral messages `{type:"typing",conversationId,isTyping:true}`; server messages `{type:"typing",conversationId,userId,displayName,isTyping:true}` when supported. Presence is optional and capability gated; it never controls message access.
- Session revocation must disconnect or cause periodic revalidation; every protected sync/read/write must revalidate current identity/membership.

## Feature capabilities

`capabilities` is a map of booleans: `auth.public_signup`, `im.dm.create`, `im.group.create`, `im.channel.create`, `im.message.edit`, `im.message.retract`, `im.typing.publish`, `im.presence.publish`, `im.attachments.upload`, `im.reactions.create`, `im.threads.create`. Capability=true requires both implemented functionality and an enabled backend flag plus user permission. Persisted data is still returned if its write flag is turned off.

## Operational contract

Identity: HTTP `:8081`, Kitex RPC `:8881`; Chat: HTTP `:8082`, Kitex RPC `:8882`; Realtime: HTTP/WS `:8083`. Each exposes `/healthz` (liveness), `/readyz` (dependencies), `/version` with build SHA/version. Use env `HTTP_ADDR`, `RPC_ADDR`, `IDENTITY_RPC_ADDR`, `CHAT_RPC_ADDR`, `DATABASE_URL`, `REDIS_ADDR`, `APP_ORIGIN`, `COOKIE_SECURE`, `REGISTRATION_CODE`, `FEATURE_FLAGS_FILE`. Runtime secrets may use env backed by protected host config; never bake them into images. Each service applies its own embedded, forward-only idempotent migrations before readiness. Release scripts serialize migration and rollout.

Caddy routes `/api/v1/auth/*` to Identity, `/api/v1/ws` to Realtime, other `/api/*` to Chat, and remaining requests to the static web container. Frontend uses same-origin relative URLs. Build context for backend Dockerfile is repository root, build arg `SERVICE=identity|chat|realtime`; executable packages are `./services/<service>/cmd`. Web build context is repository root, Dockerfile `apps/web/Dockerfile`.
