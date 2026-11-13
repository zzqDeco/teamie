/** Writes text to the clipboard or throws when the Clipboard API is unavailable/denied. */
export async function copyToClipboard(text: string) {
  if (!navigator.clipboard?.writeText) throw new Error("clipboard unavailable");
  await navigator.clipboard.writeText(text);
}
