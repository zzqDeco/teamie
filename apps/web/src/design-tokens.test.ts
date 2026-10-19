import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { ICON } from "./icons";

const srcDir = dirname(fileURLToPath(import.meta.url));
const cssFiles = [
  "tokens.css",
  "styles.css",
  "layout.css",
  "attention.css",
  "attachments.css",
  "connection-banner.css",
  "conversation-search.css",
  "mentions.css",
  "password-recovery.css",
];

function parseHex(hex: string) {
  const clean = hex.replace("#", "").trim().toLowerCase();
  if (clean.length === 3) {
    return {
      r: parseInt(clean[0] + clean[0], 16),
      g: parseInt(clean[1] + clean[1], 16),
      b: parseInt(clean[2] + clean[2], 16),
    };
  }
  if (clean.length === 6) {
    return {
      r: parseInt(clean.slice(0, 2), 16),
      g: parseInt(clean.slice(2, 4), 16),
      b: parseInt(clean.slice(4, 6), 16),
    };
  }
  return null;
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
  const a = parseHex(hexA)!;
  const b = parseHex(hexB)!;
  const la = luminance(a);
  const lb = luminance(b);
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

describe("PR-4 design tokens & WCAG AA contrast", () => {
  const tokensCss = readFileSync(join(srcDir, "tokens.css"), "utf8");
  const tokenMap = new Map<string, string>();
  for (const match of tokensCss.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) {
    tokenMap.set(match[1], match[2].trim());
  }

  it("defines complete color, surface, typography, radius, shadow, and spacing tokens", () => {
    const required = [
      "--text-strong",
      "--text",
      "--text-muted",
      "--text-subtle",
      "--brand",
      "--brand-muted",
      "--brand-text",
      "--brand-strong",
      "--focus-ring",
      "--font-sans",
      "--font-mono",
      "--bg",
      "--surface",
      "--surface-sunken",
      "--rail",
      "--lavender",
      "--border",
      "--border-strong",
      "--r-xs",
      "--r-sm",
      "--r-md",
      "--r-lg",
      "--r-xl",
      "--r-pill",
      "--shadow-sm",
      "--shadow-md",
      "--shadow-lg",
      "--shadow-pop",
      "--space-1",
      "--space-2",
      "--space-3",
      "--space-4",
      "--space-5",
      "--space-6",
      "--space-8",
      "--space-12",
    ];
    for (const name of required) {
      expect(tokenMap.has(name), `missing token ${name}`).toBe(true);
    }
    expect(tokensCss).toContain("font-family: var(--font-sans);");
    expect(tokensCss).toContain("color: var(--text-strong);");
    expect(tokensCss).toContain("background: var(--bg);");
    expect(tokensCss).toContain("font-size: var(--font-md);");
  });

  it("ensures every text token meets WCAG AA >= 4.5:1 across all light surfaces", () => {
    const surfaces = [
      "#ffffff",
      tokenMap.get("--bg")!,
      tokenMap.get("--surface-sunken")!,
      tokenMap.get("--rail")!,
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
      const fg = tokenMap.get(token)!;
      for (const bg of surfaces) {
        expect(
          contrastRatio(fg, bg),
          `${token} (${fg}) on ${bg}`,
        ).toBeGreaterThanOrEqual(4.5);
      }
    }
    expect(
      contrastRatio(tokenMap.get("--brand")!, "#ffffff"),
    ).toBeGreaterThanOrEqual(4.5);
  });

  it("eliminates sub-11px and fractional font sizes, non-standard font weights, and unmapped low-contrast text hexes", () => {
    const decorativeAllowlist = new Set(["#e2daef", "#e4d9ed", "#e2d3ee"]);
    for (const file of cssFiles) {
      const content = readFileSync(join(srcDir, file), "utf8");
      for (const match of content.matchAll(/font-size\s*:\s*([^;}\n]+)/gi)) {
        const pxMatch = /^(\d+(?:\.\d+)?)px/.exec(match[1].trim());
        if (pxMatch) {
          const px = Number(pxMatch[1]);
          expect(px, `${file}: font-size ${match[1]}`).toBeGreaterThanOrEqual(
            11,
          );
          expect(
            Number.isInteger(px),
            `${file}: fractional font-size ${match[1]}`,
          ).toBe(true);
        }
      }
      for (const match of content.matchAll(/font-weight\s*:\s*(\d+)/gi)) {
        const weight = Number(match[1]);
        expect(
          [400, 500, 600, 700].includes(weight),
          `${file}: non-standard font-weight ${weight}`,
        ).toBe(true);
      }
      if (file === "tokens.css") continue;
      for (const match of content.matchAll(
        /(?<![-\w])color\s*:\s*(#[0-9a-fA-F]{3,8})/g,
      )) {
        const hex = match[1].toLowerCase();
        if (decorativeAllowlist.has(hex)) continue;
        const rgb = parseHex(hex);
        if (!rgb) continue;
        // Any remaining literal hex must be white/light on a dark button/badge surface
        expect(
          luminance(rgb),
          `${file}: unmapped text color ${hex}`,
        ).toBeGreaterThan(0.62);
      }
    }
  });
});

describe("PR-5 CSS cleanup, unified focus, icon scale & self-hosted DM Sans", () => {
  it("contains zero unreferenced CSS classes in styles.css and layout.css", () => {
    const allFiles = readdirSync(srcDir);
    const tsFiles = allFiles.filter(
      (f) => f.endsWith(".ts") || f.endsWith(".tsx"),
    );
    let corpus =
      readFileSync(join(srcDir, "..", "index.html"), "utf8") + "\n";
    for (const f of tsFiles) {
      corpus += readFileSync(join(srcDir, f), "utf8") + "\n";
    }
    for (const cssFile of ["styles.css", "layout.css"]) {
      const content = readFileSync(join(srcDir, cssFile), "utf8");
      const classes = new Set<string>();
      for (const m of content.matchAll(/\.([a-zA-Z_-][a-zA-Z0-9_-]*)/g)) {
        classes.add(m[1]);
      }
      const unreferenced = [...classes].filter((cls) => !corpus.includes(cls));
      expect(unreferenced, `${cssFile} unreferenced classes`).toEqual([]);
    }
  });

  it("self-hosts Latin DM Sans, keeps Chinese on system fonts, and removes external Google Fonts @import", () => {
    const mainTsx = readFileSync(join(srcDir, "main.tsx"), "utf8");
    const tokensCss = readFileSync(join(srcDir, "tokens.css"), "utf8");
    expect(mainTsx).toContain('import "@fontsource-variable/dm-sans"');
    for (const file of cssFiles) {
      const content = readFileSync(join(srcDir, file), "utf8");
      expect(content).not.toContain("fonts.googleapis.com");
    }
    expect(tokensCss).toContain('"DM Sans Variable"');
    expect(tokensCss).toContain('"DM Sans Fallback"');
    expect(tokensCss).toContain('"PingFang SC"');
    expect(tokensCss).toContain('"Microsoft YaHei UI"');
    expect(tokensCss).toContain('"Noto Sans CJK SC"');
  });

  it("unifies :focus-visible across interactive controls and eliminates non-motion !important rules", () => {
    const tokensCss = readFileSync(join(srcDir, "tokens.css"), "utf8");
    expect(tokensCss).toContain("select:focus-visible");
    expect(tokensCss).toContain("summary:focus-visible");
    expect(tokensCss).toContain("outline: var(--focus-ring);");
    for (const file of cssFiles) {
      if (file === "tokens.css") continue;
      const content = readFileSync(join(srcDir, file), "utf8");
      expect(content, `${file} should have no !important`).not.toContain(
        "!important",
      );
    }
  });

  it("standardizes Lucide icon sizes onto ICON scale {14, 16, 18, 20, 24}", () => {
    expect(ICON).toEqual({
      xs: 14,
      sm: 16,
      md: 18,
      lg: 20,
      xl: 24,
    });
    const tsxFiles = readdirSync(srcDir).filter(
      (f) => f.endsWith(".tsx") && !f.endsWith(".test.tsx"),
    );
    for (const file of tsxFiles) {
      const content = readFileSync(join(srcDir, file), "utf8");
      expect(
        [...content.matchAll(/size=\{\d+\}/g)],
        `${file} raw numeric icon size`,
      ).toEqual([]);
    }
  });
});
