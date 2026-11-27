const TAB_ID_KEY = "teamie.tabId.v1";
const BOUND_MESSAGES_PREFIX = "teamie.offline.boundMessages.v1.";
const SYNC_SNAPSHOT_PREFIX = "teamie.sync.v1.";
const BIND_LEASE_TTL_MS = 15_000;
const BOUND_MESSAGE_TTL_MS = 23 * 60 * 60 * 1000;

type BoundMessageEntry = {
  tabId: string;
  state: "binding" | "queued";
  updatedAt: number;
  expiresAt?: number;
};

const inFlightBindMessageIds = new Set<string>();
let fallbackTabId = "";
let testTabIdOverride: string | null = null;

function getTabId(): string {
  if (testTabIdOverride) return testTabIdOverride;
  try {
    const existing = sessionStorage.getItem(TAB_ID_KEY);
    if (existing) return existing;
    const created =
      typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
        ? crypto.randomUUID()
        : `tab-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    sessionStorage.setItem(TAB_ID_KEY, created);
    return created;
  } catch {
    if (!fallbackTabId) {
      fallbackTabId =
        typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
          ? crypto.randomUUID()
          : `tab-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    }
    return fallbackTabId;
  }
}

export function setOfflineAttachmentTabIdForTest(tabId: string | null): void {
  testTabIdOverride = tabId;
}

export function clearInFlightStagedBindsForTest(): void {
  inFlightBindMessageIds.clear();
}

function boundKey(userId: string): string {
  return `${BOUND_MESSAGES_PREFIX}${userId}`;
}

function readBoundMap(userId: string): Record<string, BoundMessageEntry> | null {
  try {
    const raw = localStorage.getItem(boundKey(userId));
    if (!raw) return {};
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    return parsed as Record<string, BoundMessageEntry>;
  } catch {
    return null;
  }
}

function writeBoundMap(
  userId: string,
  map: Record<string, BoundMessageEntry>,
): void {
  try {
    if (Object.keys(map).length === 0) {
      localStorage.removeItem(boundKey(userId));
    } else {
      localStorage.setItem(boundKey(userId), JSON.stringify(map));
    }
  } catch {
    // Best-effort cross-tab durability.
  }
}

function pruneMap(
  map: Record<string, BoundMessageEntry>,
  now: number,
): Record<string, BoundMessageEntry> {
  const next: Record<string, BoundMessageEntry> = {};
  for (const [cmid, entry] of Object.entries(map)) {
    if (!entry || typeof entry !== "object") continue;
    if (entry.state === "queued") {
      if (now - entry.updatedAt < BOUND_MESSAGE_TTL_MS) next[cmid] = entry;
    } else if (entry.state === "binding") {
      if ((entry.expiresAt ?? 0) > now) next[cmid] = entry;
    }
  }
  return next;
}

/** Marks a staged-attachment bind as in-flight both in-memory and across tabs. */
export function beginStagedMessageBind(
  userId: string,
  clientMessageId: string,
  now = Date.now(),
): void {
  if (!clientMessageId) return;
  inFlightBindMessageIds.add(clientMessageId);
  if (!userId) return;
  const current = readBoundMap(userId) ?? {};
  const next = pruneMap(current, now);
  next[clientMessageId] = {
    tabId: getTabId(),
    state: "binding",
    updatedAt: now,
    expiresAt: now + BIND_LEASE_TTL_MS,
  };
  writeBoundMap(userId, next);
}

/** Promotes an in-flight bind to durable queued ownership once the message is persisted. */
export function markStagedMessageQueued(
  userId: string,
  clientMessageId: string,
  now = Date.now(),
): void {
  if (!clientMessageId) return;
  inFlightBindMessageIds.delete(clientMessageId);
  if (!userId) return;
  const current = readBoundMap(userId) ?? {};
  const next = pruneMap(current, now);
  next[clientMessageId] = {
    tabId: getTabId(),
    state: "queued",
    updatedAt: now,
  };
  writeBoundMap(userId, next);
}

/** Ends an in-flight bind; clears the cross-tab lease if it was not promoted to queued. */
export function endStagedMessageBind(
  userId: string,
  clientMessageId: string,
): void {
  inFlightBindMessageIds.delete(clientMessageId);
  if (!userId || !clientMessageId) return;
  const current = readBoundMap(userId);
  if (!current || current[clientMessageId]?.state !== "binding") return;
  delete current[clientMessageId];
  writeBoundMap(userId, current);
}

/** Releases cross-tab ownership when a queued message is sent, canceled, or edited. */
export function releaseStagedMessageOwnership(
  userId: string,
  clientMessageId: string,
): void {
  if (!userId || !clientMessageId) return;
  const current = readBoundMap(userId);
  if (!current || !(clientMessageId in current)) return;
  delete current[clientMessageId];
  writeBoundMap(userId, current);
}

export function clearStagedMessageOwnershipForUser(userId?: string): void {
  try {
    if (userId) {
      localStorage.removeItem(boundKey(userId));
      return;
    }
    const keys: string[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (k?.startsWith(BOUND_MESSAGES_PREFIX)) keys.push(k);
    }
    for (const k of keys) localStorage.removeItem(k);
  } catch {
    // Ignore cleanup failure.
  }
}

export function clearStagedMessageOwnershipExceptUser(userId: string): void {
  try {
    const keep = boundKey(userId);
    const keys: string[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (k?.startsWith(BOUND_MESSAGES_PREFIX) && k !== keep) keys.push(k);
    }
    for (const k of keys) localStorage.removeItem(k);
  } catch {
    // Ignore cleanup failure.
  }
}

/**
 * Collects all clientMessageIds actively owned by this tab or any sibling tab
 * (from in-memory active IDs, persisted sync snapshot, and cross-tab bind/queued
 * registry). Returns `null` if storage cannot be reliably read so reconciliation
 * fails closed rather than reclaiming live bindings.
 */
export function collectCrossTabProtectedMessageIds(
  userId: string,
  localActiveIds: ReadonlySet<string>,
  now = Date.now(),
): Set<string> | null {
  const protectedIds = new Set(localActiveIds);
  for (const id of inFlightBindMessageIds) protectedIds.add(id);

  // 1. Read persisted sync snapshot from localStorage (captures sibling tabs' flushed queues).
  try {
    const rawSync = localStorage.getItem(`${SYNC_SNAPSHOT_PREFIX}${userId}`);
    if (rawSync) {
      const parsed = JSON.parse(rawSync) as {
        messages?: Record<
          string,
          Array<{ clientMessageId?: string; status?: string }>
        >;
      };
      if (parsed?.messages && typeof parsed.messages === "object") {
        for (const list of Object.values(parsed.messages)) {
          if (!Array.isArray(list)) continue;
          for (const msg of list) {
            if (
              msg?.clientMessageId &&
              (msg.status === "queued" ||
                msg.status === "sending" ||
                msg.status === "failed")
            ) {
              protectedIds.add(msg.clientMessageId);
            }
          }
        }
      }
    }
  } catch {
    return null;
  }

  // 2. Read durable cross-tab bound/binding ownership registry.
  const boundMap = readBoundMap(userId);
  if (boundMap === null) return null;
  const tabId = getTabId();
  let mutated = false;
  const cleaned: Record<string, BoundMessageEntry> = {};
  for (const [cmid, entry] of Object.entries(boundMap)) {
    if (!entry || typeof entry !== "object") {
      mutated = true;
      continue;
    }
    if (entry.state === "queued") {
      if (now - entry.updatedAt < BOUND_MESSAGE_TTL_MS) {
        protectedIds.add(cmid);
        cleaned[cmid] = entry;
      } else {
        mutated = true;
      }
    } else if (entry.state === "binding") {
      const expired = (entry.expiresAt ?? 0) <= now;
      const staleSameTab =
        entry.tabId === tabId && !inFlightBindMessageIds.has(cmid);
      if (!expired && !staleSameTab) {
        protectedIds.add(cmid);
        cleaned[cmid] = entry;
      } else {
        mutated = true;
      }
    } else {
      mutated = true;
    }
  }
  if (mutated) writeBoundMap(userId, cleaned);

  return protectedIds;
}
