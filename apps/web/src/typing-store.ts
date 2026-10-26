import { useCallback, useSyncExternalStore } from "react";

export type TypingUser = { userId: string; displayName: string; until: number };
export type TypingState = Record<string, TypingUser[]>;

const TYPING_TTL_MS = 5000;
const noTypingUsers: TypingUser[] = [];

// Ephemeral typing hints live outside ChatContext so a hint re-renders only
// the indicators subscribed to its conversation, never every useChat consumer.
let state: TypingState = {};
const listeners = new Set<() => void>();

function set(next: TypingState) {
  if (next === state) return;
  state = next;
  listeners.forEach((listener) => listener());
}

export const typingStore = {
  getSnapshot: () => state,
  subscribe(listener: () => void) {
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  },
  /** Applies a typing hint from another participant, refreshing its TTL. */
  hint(conversationId: string, userId: string, displayName: string, isTyping: boolean) {
    set({
      ...state,
      [conversationId]: [
        ...(state[conversationId] || []).filter((x) => x.userId !== userId),
        ...(isTyping
          ? [{ userId, displayName, until: Date.now() + TYPING_TTL_MS }]
          : []),
      ],
    });
  },
  /** Drops expired publishers (e.g. a crashed client that never sent stop). */
  expire(now = Date.now()) {
    let changed = false;
    const next: TypingState = {};
    for (const [id, entries] of Object.entries(state)) {
      const active = entries.filter((x) => x.until > now);
      if (active.length !== entries.length || active.length === 0)
        changed = true;
      if (active.length)
        next[id] = active.length === entries.length ? entries : active;
    }
    if (changed) set(next);
  },
  reset() {
    if (Object.keys(state).length) set({});
  },
};

/** Other participants currently typing in `conversationId`. */
export function useTypingUsers(conversationId: string): TypingUser[] {
  return useSyncExternalStore(
    typingStore.subscribe,
    useCallback(
      () => typingStore.getSnapshot()[conversationId] || noTypingUsers,
      [conversationId],
    ),
  );
}
