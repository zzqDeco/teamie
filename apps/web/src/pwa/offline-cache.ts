import type {
  Bootstrap,
  Conversation,
  Member,
  User,
  Workspace,
} from "@teamie/api-client";

const META_KEY = "teamie.offline.meta.v1";
const SHELL_PREFIX = "teamie.offline.shell.v1.";
const PENDING_LOGOUT_KEY = "teamie.offline.pendingLogout.v1";
const shellKey = (userId: string) => `${SHELL_PREFIX}${userId}`;

export type PendingOfflineLogout = {
  userId: string;
  requestedAt: string;
};

export type OfflineShellSnapshot = {
  version: 1;
  savedAt: string;
  user: User;
  workspaces: Workspace[];
  conversations: Conversation[];
  membersByWorkspace: Record<string, Member[]>;
  capabilities: Record<string, boolean>;
  flagsRevision: string;
};

function getStorage(): Storage | null {
  try {
    return typeof window !== "undefined" && window.localStorage
      ? window.localStorage
      : null;
  } catch {
    return null;
  }
}

function sanitizeUser(user: User): User {
  return {
    id: user.id,
    email: user.email,
    displayName: user.displayName,
    kind: user.kind,
    platformRole: user.platformRole,
    createdAt: user.createdAt,
  };
}

function isValidUser(value: unknown): value is User {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Partial<User>;
  return (
    typeof candidate.id === "string" &&
    candidate.id.length > 0 &&
    typeof candidate.email === "string" &&
    typeof candidate.displayName === "string" &&
    (candidate.kind === "human" || candidate.kind === "agent") &&
    (candidate.platformRole === "admin" || candidate.platformRole === "user") &&
    typeof candidate.createdAt === "string"
  );
}

function readMetaUserId(storage: Storage): string | null {
  try {
    const raw = storage.getItem(META_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as { userId?: unknown };
    return typeof parsed?.userId === "string" && parsed.userId
      ? parsed.userId
      : null;
  } catch {
    return null;
  }
}

export function readOfflineShellSnapshot(
  userId?: string,
): OfflineShellSnapshot | null {
  const storage = getStorage();
  if (!storage) return null;
  const targetUserId = userId || readMetaUserId(storage);
  if (!targetUserId) return null;
  try {
    const raw = storage.getItem(shellKey(targetUserId));
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<OfflineShellSnapshot>;
    if (
      parsed?.version !== 1 ||
      !isValidUser(parsed.user) ||
      parsed.user.id !== targetUserId ||
      !Array.isArray(parsed.workspaces) ||
      !Array.isArray(parsed.conversations)
    ) {
      return null;
    }
    const membersByWorkspace: Record<string, Member[]> = {};
    if (
      parsed.membersByWorkspace &&
      typeof parsed.membersByWorkspace === "object"
    ) {
      for (const [wsId, list] of Object.entries(parsed.membersByWorkspace)) {
        if (Array.isArray(list)) {
          membersByWorkspace[wsId] = list.slice(0, 500);
        }
      }
    }
    const capabilities: Record<string, boolean> = {};
    if (parsed.capabilities && typeof parsed.capabilities === "object") {
      for (const [k, v] of Object.entries(parsed.capabilities)) {
        if (typeof v === "boolean") capabilities[k] = v;
      }
    }
    return {
      version: 1,
      savedAt:
        typeof parsed.savedAt === "string"
          ? parsed.savedAt
          : new Date(0).toISOString(),
      user: sanitizeUser(parsed.user),
      workspaces: parsed.workspaces,
      conversations: parsed.conversations,
      membersByWorkspace,
      capabilities,
      flagsRevision:
        typeof parsed.flagsRevision === "string" ? parsed.flagsRevision : "",
    };
  } catch {
    return null;
  }
}

function writeSnapshot(snapshot: OfflineShellSnapshot): void {
  const storage = getStorage();
  if (!storage) return;
  try {
    const previousUserId = readMetaUserId(storage);
    if (previousUserId && previousUserId !== snapshot.user.id) {
      storage.removeItem(shellKey(previousUserId));
    }
    storage.setItem(shellKey(snapshot.user.id), JSON.stringify(snapshot));
    storage.setItem(
      META_KEY,
      JSON.stringify({ userId: snapshot.user.id, savedAt: snapshot.savedAt }),
    );
  } catch {
    // Ignore storage quota or private-mode restrictions.
  }
}

/**
 * Persists only non-sensitive profile metadata for Warm Offline Boot.
 * Never persists CSRF tokens, cookies, passwords, or secrets.
 */
export function saveOfflineUser(user: User): void {
  const existing = readOfflineShellSnapshot(user.id);
  writeSnapshot({
    version: 1,
    savedAt: new Date().toISOString(),
    user: sanitizeUser(user),
    workspaces: existing?.workspaces || [],
    conversations: existing?.conversations || [],
    membersByWorkspace: existing?.membersByWorkspace || {},
    capabilities: existing?.capabilities || {},
    flagsRevision: existing?.flagsRevision || "",
  });
}

/**
 * Persists authorized workspaces, conversations, and capability hints from an
 * authoritative bootstrap response.
 */
export function saveOfflineBootstrap(bootstrap: Bootstrap): void {
  const existing = readOfflineShellSnapshot(bootstrap.user.id);
  const allowedWorkspaceIds = new Set(bootstrap.workspaces.map((w) => w.id));
  const membersByWorkspace: Record<string, Member[]> = {};
  if (existing?.membersByWorkspace) {
    for (const [wsId, list] of Object.entries(existing.membersByWorkspace)) {
      if (allowedWorkspaceIds.has(wsId)) {
        membersByWorkspace[wsId] = list;
      }
    }
  }
  writeSnapshot({
    version: 1,
    savedAt: new Date().toISOString(),
    user: sanitizeUser(bootstrap.user),
    workspaces: bootstrap.workspaces,
    conversations: bootstrap.conversations,
    membersByWorkspace,
    capabilities: { ...bootstrap.capabilities },
    flagsRevision: bootstrap.flagsRevision,
  });
}

export function saveOfflineWorkspaceMembers(
  userId: string,
  workspaceId: string,
  members: Member[],
): void {
  const existing = readOfflineShellSnapshot(userId);
  if (!existing) return;
  if (!existing.workspaces.some((w) => w.id === workspaceId)) return;
  writeSnapshot({
    ...existing,
    savedAt: new Date().toISOString(),
    membersByWorkspace: {
      ...existing.membersByWorkspace,
      [workspaceId]: members.slice(0, 500),
    },
  });
}

export function readOfflineBootstrap(userId: string): Bootstrap | null {
  const snapshot = readOfflineShellSnapshot(userId);
  if (!snapshot) return null;
  return {
    user: snapshot.user,
    workspaces: snapshot.workspaces,
    conversations: snapshot.conversations,
    capabilities: snapshot.capabilities,
    flagsRevision: snapshot.flagsRevision,
    syncCursor: "0",
  };
}

export function readOfflineWorkspaceMembers(
  userId: string,
  workspaceId: string,
): Member[] {
  const snapshot = readOfflineShellSnapshot(userId);
  return snapshot?.membersByWorkspace[workspaceId] || [];
}

/**
 * Immediately clears offline shell snapshots on logout, 401/403 expiration,
 * password change, or account switch.
 */
export function clearOfflineShellSnapshot(userId?: string): void {
  const storage = getStorage();
  if (!storage) return;
  try {
    const metaUserId = readMetaUserId(storage);
    if (userId) {
      storage.removeItem(shellKey(userId));
      if (metaUserId === userId) {
        storage.removeItem(META_KEY);
      }
      return;
    }
    if (metaUserId) {
      storage.removeItem(shellKey(metaUserId));
    }
    storage.removeItem(META_KEY);
    for (let i = storage.length - 1; i >= 0; i--) {
      const key = storage.key(i);
      if (key && key.startsWith(SHELL_PREFIX)) {
        storage.removeItem(key);
      }
    }
  } catch {
    // Ignore storage access errors.
  }
}

export function markPendingOfflineLogout(userId: string): void {
  const storage = getStorage();
  if (!storage || !userId) return;
  try {
    const payload: PendingOfflineLogout = {
      userId,
      requestedAt: new Date().toISOString(),
    };
    storage.setItem(PENDING_LOGOUT_KEY, JSON.stringify(payload));
  } catch {
    // Ignore storage access errors.
  }
}

export function readPendingOfflineLogout(): PendingOfflineLogout | null {
  const storage = getStorage();
  if (!storage) return null;
  try {
    const raw = storage.getItem(PENDING_LOGOUT_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<PendingOfflineLogout>;
    if (typeof parsed?.userId === "string" && parsed.userId) {
      return {
        userId: parsed.userId,
        requestedAt:
          typeof parsed.requestedAt === "string" ? parsed.requestedAt : "",
      };
    }
    return null;
  } catch {
    return null;
  }
}

export function clearPendingOfflineLogout(): void {
  const storage = getStorage();
  if (!storage) return;
  try {
    storage.removeItem(PENDING_LOGOUT_KEY);
  } catch {
    // Ignore storage access errors.
  }
}
