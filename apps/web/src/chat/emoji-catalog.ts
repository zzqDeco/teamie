// Loaded only through dynamic import() (see loadEmojiCatalog) so the full
// Unicode Emoji 17.0 data never lands in the main bundle.
import { EMOJI_GROUPS, EMOJI_VARIANTS } from "./emoji-data.generated";

export { EMOJI_GROUPS };

const allowed = new Set<string>(EMOJI_VARIANTS.split(" "));
for (const group of EMOJI_GROUPS) for (const [emoji] of group.entries) allowed.add(emoji);
const encoder = new TextEncoder();

/**
 * Contract rule for reaction emoji: exactly one fully-qualified Unicode
 * Emoji 17.0 sequence from the pinned data, at most 64 UTF-8 bytes.
 */
export function isReactionEmoji(value: unknown): value is string {
  return (
    typeof value === "string" &&
    encoder.encode(value).length <= 64 &&
    allowed.has(value)
  );
}

export const allowedEmojiCount = () => allowed.size;

const SEARCH_GROUPS = EMOJI_GROUPS.map((group) => ({
  labelLower: group.label.toLocaleLowerCase(),
  entries: group.entries.map(
    ([emoji, name]) => [emoji, name.toLocaleLowerCase()] as const,
  ),
}));

/** Name/category search over picker entries (English CLDR names + Chinese group labels). */
export function searchEmojis(query: string, limit = 160): string[] {
  const needle = query.trim().toLocaleLowerCase();
  if (!needle) return [];
  const results: string[] = [];
  for (const group of SEARCH_GROUPS) {
    const groupMatch = group.labelLower.includes(needle);
    for (const [emoji, nameLower] of group.entries) {
      if (groupMatch || nameLower.includes(needle) || emoji === needle) {
        results.push(emoji);
        if (results.length >= limit) return results;
      }
    }
  }
  return results;
}
