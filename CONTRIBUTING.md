# Working on Teamie

Read `AGENTS.md`, the architecture and HTTP contract before changing behavior. Keep IM authentication, membership, messages and runtime execution in their declared owners.

Develop in a short `feat/*` or `fix/*` branch and open a pull request into `main`. Keep contracts, generated RPC code, implementation and client changes together. Run the relevant checks and describe their evidence. A local build, passing CI, a published image and a healthy deployment are separate claims.

Feature flags permit incomplete optional functionality to land on main safely. Gate writes on the backend; keep previously persisted content readable. A frontend flag is not an authorization control. Account-switch/logout paths must remove the previous account's cached state.

Do not create a second persistent product environment. Local temporary processes and disposable CI dependencies are allowed for verification. Never use production credentials in CI database tests.

Cut a new release branch from the exact verified main commit. The versioned release branch is an archive immediately; fix any failed release through main and a new version. Do not rewrite an old branch to make its checks green. Do not delete release branches when cleaning feature branches.

Database changes must remain compatible with the documented application rollback boundary. Never pair an image rollback with an automatic database restore. Keep service ownership and membership changes consistent under concurrent message writes.
