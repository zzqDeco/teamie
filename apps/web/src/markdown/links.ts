/** Presentation details for a sanitized message link. */
export type LinkPresentation = {
  /** Normalized href (URL serializer: punycode host, encoded path). */
  href: string;
  /** Same-origin `/app/...` path navigated inside the SPA, else undefined. */
  appPath?: string;
  /** Host shown after the label when the label does not name that host. */
  hostSuffix?: string;
};

function bareHost(value: string) {
  return value
    .trim()
    .toLowerCase()
    .replace(/^[a-z][a-z0-9+.-]*:\/\//, "")
    .replace(/^www\./, "");
}

export function linkPresentation(safeUrl: string, labelText?: string): LinkPresentation {
  let parsed: URL;
  try {
    parsed = new URL(safeUrl);
  } catch {
    return { href: safeUrl };
  }
  const href = parsed.href;
  if (parsed.protocol === "mailto:") return { href };
  if (
    typeof window !== "undefined" &&
    parsed.origin === window.location.origin &&
    (parsed.pathname === "/app" || parsed.pathname.startsWith("/app/"))
  ) {
    return { href, appPath: `${parsed.pathname}${parsed.search}${parsed.hash}` };
  }
  if (labelText === undefined) return { href };
  const host = parsed.hostname.toLowerCase().replace(/^www\./, "");
  const label = bareHost(labelText);
  const namesHost = label === host || label.startsWith(`${host}/`) || label.startsWith(`${host}:`);
  return namesHost ? { href } : { href, hostSuffix: host };
}
