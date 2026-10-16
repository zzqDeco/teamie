# Teamie web

React + TypeScript + Vite client. Node 24 LTS is used for production builds.

```sh
npm ci
npm run dev
npm run lint
npm test
npm run build
```

The Vite server proxies same-origin `/api` requests to local Identity (`8081`), Chat (`8082`) and Realtime (`8083`), Agent Control (`8084`) and Model Gateway (`8086`). Override the proxy targets with `IDENTITY_HTTP_ORIGIN`, `CHAT_HTTP_ORIGIN`, `REALTIME_HTTP_ORIGIN`, `CONTROLPLANE_HTTP_ORIGIN` and `GATEWAY_HTTP_ORIGIN` when needed. Set the services' `APP_ORIGIN` to the exact browser origin. These commands run local development; production is the only deployed environment.

`apps/web/Dockerfile` uses repository-root build context and serves the SPA on port 80. `/healthz` reports liveness; `/version.json` contains the `VERSION` and `COMMIT_SHA` build arguments. The external edge owns `/api` routing.

## Accounts and invitations

Registration always requires an invitation. `/login?mode=register&inviteCode=…` pre-fills the code and preserves it when switching login tabs. Public `/api/v1/auth/registration` metadata explains whether the installation still needs its first account; it never exposes invitation secrets. The first successfully registered account uses the deployment's bootstrap invitation and becomes platform administrator. That bootstrap invitation cannot create later accounts.

Platform administrators manage ordinary registration invitations at `/app/admin`. Each generated invitation can create exactly one account and expires after seven days. Its raw code and registration link are held only in the current component's memory, displayed after creation and never added to the query cache or persistent sync storage. The record list contains timestamps/status only; available invitations can be revoked. Backend authorization remains authoritative for every operation.

A platform registration invitation creates an account. A workspace invitation admits an existing account to a workspace. Platform administrator and workspace owner are separate roles; platform administration grants no access to other workspaces' conversations.

## Messaging state

Durable HTTP sync starts at cursor `0`; the bootstrap cursor is never adopted. Applied message snapshots and the sync cursor persist together under the authenticated account. WebSocket frames trigger HTTP synchronization, with reconnect backoff and periodic fallback. Logout/account changes clear protected caches; membership denials reset them. Message reconciliation preserves immutable sequence order, idempotent client IDs, nanosecond edit versions and retraction tombstones.

## Agents and model configuration

`/app/agents` lists the signed-in human's Agents. Owners can edit the name, public description, visibility within their current workspaces, and enabled state, or delete an Agent. Platform admin does not bypass Agent ownership. `/app/w/:workspaceId/agents` discovers visible Agent participants; people and Agents share the IM member picker and private-group membership controls. Agent badges and available owner provenance remain visible in messages and member lists.

The model gateway is managed only at `/app/admin/gateway` by human platform administrators. The API key stays in a password input's component state until submission, is never added to URLs/local storage/Query variables, and GET/PUT responses are projected to public metadata before caching. Blank keys preserve existing credentials; clearing one is explicit. Saved configuration is distinct from verified provider availability. The separately labeled connection test sends one real request using saved configuration; it never runs automatically.

Production starts unconfigured at the user's request. Agent status and conversation banners explicitly show waiting for administrator configuration, with no fabricated model replies. Owner pages expose basic profile/team visibility and status only, with no model, prompt, tool, or budget controls.
