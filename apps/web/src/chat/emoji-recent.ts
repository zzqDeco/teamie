type EmojiCatalog = typeof import("./emoji-catalog");
let catalog: Promise<EmojiCatalog> | null = null;
let loaded: EmojiCatalog | null = null;

/** Lazily loads the emoji catalog chunk (Unicode Emoji 17.0 data). */
export function loadEmojiCatalog(): Promise<EmojiCatalog> {
  catalog ??= import("./emoji-catalog").then(
    (module) => (loaded = module),
    (error: unknown) => {
      catalog = null;
      throw error;
    },
  );
  return catalog;
}
export const loadedEmojiCatalog = () => loaded;

const MAX_RECENT = 16;
const recentKey = (userId: string) => `teamie.reactions.recent.v1:${userId}`;

/** Raw recents for this account; callers validate with the catalog before display. */
export function readRecentReactions(userId: string): string[] {
  if (!userId) return [];
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(recentKey(userId)) || "[]");
    return Array.isArray(parsed)
      ? parsed.filter((item): item is string => typeof item === "string").slice(0, MAX_RECENT)
      : [];
  } catch {
    return [];
  }
}

export function recordRecentReaction(userId: string, emoji: string) {
  if (!userId) return;
  const next = [emoji, ...readRecentReactions(userId).filter((item) => item !== emoji)].slice(
    0,
    MAX_RECENT,
  );
  try {
    localStorage.setItem(recentKey(userId), JSON.stringify(next));
  } catch {
    /* Recents are a convenience; blocked storage simply keeps none. */
  }
}

/** Recents that pass the contract allowlist (empty until the catalog loads). */
export function validRecentReactions(userId: string, current = loaded): string[] {
  if (!current) return [];
  return readRecentReactions(userId).filter((emoji) => current.isReactionEmoji(emoji));
}
