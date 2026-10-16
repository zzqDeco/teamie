# First release scope and acceptance

This release establishes a usable independent IM product and its delivery path on the user's existing Aliyun host.

Included: product entry, invitation registration/login, persistent sessions, workspace creation/invitations/membership, channels, direct and group conversations, text messages/replies/edit/retract, read state, durable reconnect sync, WebSocket hints and backend feature flags. The capability response is authoritative for optional functions.

Deferred: Eino runtime execution, Agent registration/credentials, sandboxing, media uploads, rich thread/reaction models, search, voice/video, mobile push, enterprise SSO and multi-host high availability. Visible disabled placeholders must not imply these are implemented.

Acceptance records code/build, CI, image publication, deployment, runtime health and business flows separately. Required business checks cover two independent accounts, workspace invitation, ordered messages, retry idempotence, direct/group scope, read state, reconnect synchronization, forbidden outsider access, removal revocation and logout/session revocation. Browser checks cover the real HTTP-backed landing/login/IM flow and small-screen layout. Report unverified conditions explicitly.

Only `production` is a persistent deployed environment. Disposable databases in CI are verification infrastructure, not a second product deployment. Main is the ongoing development trunk. Permanent `release/vx.x.x` branches, immutable tags, exact image digests and release manifests preserve each published version.
