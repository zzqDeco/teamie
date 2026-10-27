// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { User } from "@teamie/api-client";
import { ProfileSettings } from "./dialogs";
import {
  applyTheme,
  getThemePreference,
  setThemePreference,
  THEME_STORAGE_KEY,
} from "./theme";

const srcDir = dirname(fileURLToPath(import.meta.url));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const mockUser: User = {
  id: "u1",
  kind: "human",
  email: "lin@teamie.local",
  displayName: "林知夏",
  platformRole: "user",
  createdAt: "2026-10-18T10:00:00Z",
};

vi.mock("./auth", () => ({
  useAuth: () => ({
    user: mockUser,
    loading: false,
    logout: vi.fn(),
    updateUser: vi.fn(),
  }),
}));

function parseHex(hex: string) {
  const clean = hex.replace("#", "").trim().toLowerCase();
  return {
    r: parseInt(clean.slice(0, 2), 16),
    g: parseInt(clean.slice(2, 4), 16),
    b: parseInt(clean.slice(4, 6), 16),
  };
}

function srgbToLinear(channel: number) {
  const s = channel / 255;
  return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
}

function luminance(rgb: { r: number; g: number; b: number }) {
  return (
    0.2126 * srgbToLinear(rgb.r) +
    0.7152 * srgbToLinear(rgb.g) +
    0.0722 * srgbToLinear(rgb.b)
  );
}

function contrastRatio(hexA: string, hexB: string) {
  const la = luminance(parseHex(hexA));
  const lb = luminance(parseHex(hexB));
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

describe("PR-6 dark mode tokens, head bootstrap & settings switcher", () => {
  let darkMatches = false;
  const mediaListeners = new Set<() => void>();
  let container: HTMLDivElement | null = null;
  let root: Root | null = null;

  beforeEach(() => {
    window.localStorage.clear();
    darkMatches = false;
    mediaListeners.clear();
    Object.defineProperty(window, "matchMedia", {
      writable: true,
      configurable: true,
      value: (query: string) => ({
        matches: query.includes("prefers-color-scheme: dark")
          ? darkMatches
          : false,
        media: query,
        onchange: null,
        addListener: () => {},
        removeListener: () => {},
        addEventListener: (
          _type: string,
          cb: EventListenerOrEventListenerObject,
        ) => {
          if (typeof cb === "function") mediaListeners.add(cb as () => void);
        },
        removeEventListener: (
          _type: string,
          cb: EventListenerOrEventListenerObject,
        ) => {
          if (typeof cb === "function") mediaListeners.delete(cb as () => void);
        },
        dispatchEvent: () => true,
      }),
    });
    applyTheme();
  });

  afterEach(async () => {
    if (root) {
      await act(async () => {
        root!.unmount();
      });
      root = null;
    }
    container?.remove();
    container = null;
    vi.restoreAllMocks();
  });

  it("ensures all [data-theme=dark] text, brand, scrollbar, overlay, switch, and status tokens meet WCAG contrast", () => {
    const tokensCss = readFileSync(join(srcDir, "tokens.css"), "utf8");
    const darkBlock =
      /:root\[data-theme="dark"\]\s*\{([\s\S]*?)\}/.exec(tokensCss)?.[1] || "";
    const darkMap = new Map<string, string>();
    for (const m of darkBlock.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) {
      darkMap.set(m[1], m[2].trim());
    }

    for (const required of [
      "--scrollbar-thumb",
      "--scrollbar-thumb-hover",
      "--backdrop",
      "--switch-bg",
      "--switch-on",
      "--status-online",
      "--status-connecting",
      "--status-offline",
    ]) {
      expect(darkMap.has(required), `missing dark token ${required}`).toBe(
        true,
      );
    }

    const surfaces = [
      darkMap.get("--bg")!,
      darkMap.get("--surface")!,
      darkMap.get("--surface-sunken")!,
      darkMap.get("--rail")!,
    ];
    const textTokens = [
      "--text-strong",
      "--text",
      "--text-muted",
      "--text-subtle",
      "--brand-text",
      "--brand-strong",
      "--danger-text",
      "--warning-text",
      "--success-text",
    ];
    for (const token of textTokens) {
      const fg = darkMap.get(token)!;
      for (const bg of surfaces) {
        expect(
          contrastRatio(fg, bg),
          `dark ${token} (${fg}) on ${bg}`,
        ).toBeGreaterThanOrEqual(4.5);
      }
    }
    const nonTextTokens = [
      "--switch-bg",
      "--switch-on",
      "--status-online",
      "--status-connecting",
      "--status-offline",
    ];
    for (const token of nonTextTokens) {
      const fg = darkMap.get(token)!;
      for (const bg of [...surfaces, darkMap.get("--lavender")!]) {
        expect(
          contrastRatio(fg, bg),
          `dark ${token} (${fg}) on ${bg}`,
        ).toBeGreaterThanOrEqual(3);
      }
    }
    expect(
      contrastRatio(darkMap.get("--on-brand")!, darkMap.get("--brand")!),
    ).toBeGreaterThanOrEqual(4.5);
    expect(
      contrastRatio(darkMap.get("--on-brand")!, darkMap.get("--brand-muted")!),
    ).toBeGreaterThanOrEqual(4.5);
    for (const fill of ["--danger", "--danger-hover"]) {
      expect(
        contrastRatio(darkMap.get("--on-danger")!, darkMap.get(fill)!),
        `dark --on-danger on ${fill}`,
      ).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("includes dual light/dark theme-color meta tags and synchronous theme initialization script in index.html head", () => {
    const html = readFileSync(join(srcDir, "..", "index.html"), "utf8");
    expect(html).toMatch(
      /<meta[^>]+name="theme-color"[^>]+media="\(prefers-color-scheme:\s*light\)"[^>]+content="#7463d4"/,
    );
    expect(html).toMatch(
      /<meta[^>]+name="theme-color"[^>]+media="\(prefers-color-scheme:\s*dark\)"[^>]+content="#17141c"/,
    );
    expect(html).toContain('localStorage.getItem("teamie.theme")');
    expect(html).toContain("document.documentElement.dataset.theme");

    const scriptMatch = html.match(/<script>([\s\S]*?)<\/script>/);
    expect(scriptMatch).toBeTruthy();
    darkMatches = true;
    const getItemSpy = vi
      .spyOn(Storage.prototype, "getItem")
      .mockImplementation(() => {
        throw new DOMException("SecurityError", "SecurityError");
      });
    try {
      document.documentElement.dataset.theme = "light";
      new Function(scriptMatch![1])();
      expect(document.documentElement.dataset.theme).toBe("dark");
      expect(document.documentElement.dataset.themePreference).toBe("system");
    } finally {
      getItemSpy.mockRestore();
    }
  });

  it("switches between 跟随系统 / 浅色 / 深色 in ProfileSettings and reacts to OS color-scheme changes", async () => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    const client = new QueryClient();

    await act(async () => {
      root!.render(
        <QueryClientProvider client={client}>
          <MemoryRouter>
            <ProfileSettings />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });

    const buttons = Array.from(
      container.querySelectorAll<HTMLButtonElement>(".theme-option"),
    );
    const systemBtn = buttons.find((b) => b.textContent === "跟随系统")!;
    const lightBtn = buttons.find((b) => b.textContent === "浅色")!;
    const darkBtn = buttons.find((b) => b.textContent === "深色")!;

    expect(systemBtn.getAttribute("aria-pressed")).toBe("true");
    expect(document.documentElement.dataset.theme).toBe("light");

    await act(async () => {
      darkBtn.click();
    });
    expect(getThemePreference()).toBe("dark");
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBe("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(darkBtn.getAttribute("aria-pressed")).toBe("true");

    await act(async () => {
      lightBtn.click();
    });
    expect(getThemePreference()).toBe("light");
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(lightBtn.getAttribute("aria-pressed")).toBe("true");

    await act(async () => {
      systemBtn.click();
    });
    expect(getThemePreference()).toBe("system");
    await act(async () => {
      darkMatches = true;
      for (const cb of mediaListeners) cb();
    });
    expect(document.documentElement.dataset.theme).toBe("dark");

    await act(async () => {
      setThemePreference("light");
    });
    expect(document.documentElement.dataset.theme).toBe("light");

    const setItemSpy = vi
      .spyOn(Storage.prototype, "setItem")
      .mockImplementation(() => {
        throw new DOMException("QuotaExceededError", "QuotaExceededError");
      });
    try {
      await act(async () => {
        darkBtn.click();
      });
      expect(getThemePreference()).toBe("dark");
      expect(document.documentElement.dataset.theme).toBe("dark");
      expect(darkBtn.getAttribute("aria-pressed")).toBe("true");
    } finally {
      setItemSpy.mockRestore();
      await act(async () => {
        setThemePreference("system");
      });
    }
  });

  it("pairs brand backgrounds with --on-brand and uses --surface on mention popup", () => {
    const shellCss = readFileSync(join(srcDir, "styles", "shell.css"), "utf8");
    const chatCss = readFileSync(join(srcDir, "styles", "chat.css"), "utf8");
    const attentionCss = readFileSync(join(srcDir, "attention.css"), "utf8");
    const mentionsCss = readFileSync(join(srcDir, "mentions.css"), "utf8");

    expect(shellCss).toMatch(
      /\.unread-badge\s*\{[^}]*color:\s*var\(--on-brand\);[^}]*background:\s*var\(--brand\);/s,
    );
    expect(chatCss).toMatch(
      /\.send-button\s*\{[^}]*background:\s*var\(--purple\);[^}]*color:\s*var\(--on-brand\);/s,
    );
    expect(attentionCss).toMatch(
      /\.rail-attention-count\s*\{[^}]*background:\s*var\(--brand\);[^}]*color:\s*var\(--on-brand\);/s,
    );
    expect(mentionsCss).toMatch(
      /\.mention-options\s*\{[^}]*background:\s*var\(--surface\);/s,
    );
  });
});
