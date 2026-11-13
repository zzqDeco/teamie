import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { ICON } from "./icons";

const srcDir = dirname(fileURLToPath(import.meta.url));
const modularStyleFiles = [
  "styles/base.css",
  "styles/primitives.css",
  "styles/primitives-controls.css",
  "styles/public.css",
  "styles/auth.css",
  "styles/shell.css",
  "styles/quick-switcher.css",
  "styles/settings.css",
  "styles/chat.css",
  "styles/markdown.css",
  "styles/members.css",
  "styles/admin.css",
  "styles/agents.css",
  "styles/gateway.css",
  "styles/marketplace.css",
  "styles/utilities.css",
];
const modularLayoutFiles = [
  "styles/layout-base.css",
  "styles/layout-tablet.css",
  "styles/layout-mobile.css",
  "styles/layout-mobile-compact.css",
];
const modularFeatureFiles = [
  "styles/reactions.css",
  "styles/timeline.css",
  "styles/announcements.css",
  "styles/attention.css",
  "styles/attachments.css",
  "styles/connection-banner.css",
  "styles/conversation-search.css",
  "styles/mentions.css",
  "styles/password-recovery.css",
  "styles/feedback.css",
  "styles/conveniences.css",
];
const cssFiles = [
  "tokens.css",
  ...modularStyleFiles,
  ...modularLayoutFiles,
  ...modularFeatureFiles,
];

function listRelativeFiles(dir: string, root = dir): string[] {
  const results: string[] = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      results.push(...listRelativeFiles(full, root));
    } else {
      results.push(relative(root, full).replace(/\\/g, "/"));
    }
  }
  return results;
}

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
  const lightBlock = /:root\s*\{([\s\S]*?)\}/.exec(tokensCss)?.[1] || "";
  const tokenMap = new Map<string, string>();
  for (const match of lightBlock.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) {
    tokenMap.set(match[1], match[2].trim());
  }

  it("declares cascade @layer order and defines complete color, surface, typography, radius, shadow, spacing, z-index, and layout tokens", () => {
    expect(readFileSync(join(srcDir, "layers.css"), "utf8")).toContain(
      "@layer tokens, base, components, features, layout, utilities;",
    );
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
      "--danger",
      "--danger-hover",
      "--on-danger",
      "--scrollbar-thumb",
      "--scrollbar-thumb-hover",
      "--backdrop",
      "--switch-bg",
      "--switch-on",
      "--status-online",
      "--status-connecting",
      "--status-offline",
      "--font-sans",
      "--font-mono",
      "--font-xs",
      "--font-sm",
      "--font-base",
      "--font-md",
      "--font-lg",
      "--font-xl",
      "--font-2xl",
      "--font-3xl",
      "--font-4xl",
      "--bg",
      "--surface",
      "--surface-sunken",
      "--rail",
      "--lavender",
      "--border",
      "--border-strong",
      "--field-border",
      "--lh-tight",
      "--lh-ui",
      "--lh-body",
      "--dur-fast",
      "--dur-base",
      "--dur-slow",
      "--ease-standard",
      "--ease-emphasized",
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
      "--space-0_5",
      "--space-1",
      "--space-1_5",
      "--space-2",
      "--space-2_5",
      "--space-3",
      "--space-3_5",
      "--space-4",
      "--space-5",
      "--space-6",
      "--space-7",
      "--space-8",
      "--space-10",
      "--space-12",
      "--z-raised",
      "--z-local",
      "--z-sticky",
      "--z-banner",
      "--z-members-shade",
      "--z-members-pane",
      "--z-dropdown",
      "--z-drawer-shade",
      "--z-drawer",
      "--z-popover",
      "--z-modal",
      "--z-toast",
      "--avatar-1-bg",
      "--avatar-1-fg",
      "--avatar-2-bg",
      "--avatar-2-fg",
      "--avatar-3-bg",
      "--avatar-3-fg",
      "--avatar-4-bg",
      "--avatar-4-fg",
      "--avatar-5-bg",
      "--avatar-5-fg",
      "--avatar-6-bg",
      "--avatar-6-fg",
      "--header-h",
      "--header-h-mobile",
    ];
    for (const name of required) {
      expect(tokenMap.has(name), `missing token ${name}`).toBe(true);
    }
    expect(tokenMap.get("--font-xs")).toBe("12px");
    expect(tokenMap.get("--font-sm")).toBe("13px");
    expect(tokenMap.get("--font-base")).toBe("14px");
    expect(tokenMap.get("--font-lg")).toBe("15px");
    expect(tokensCss).toContain("font-family: var(--font-sans);");
    expect(tokensCss).toContain("color: var(--text-strong);");
    expect(tokensCss).toContain("background: var(--bg);");
    expect(tokensCss).toContain("font-size: var(--font-md);");
  });

  it("ensures every text token meets WCAG AA >= 4.5:1 and non-text UI tokens meet >= 3:1 across all light surfaces", () => {
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
    const nonTextTokens = [
      "--field-border",
      "--scrollbar-thumb",
      "--scrollbar-thumb-hover",
      "--switch-bg",
      "--switch-on",
      "--status-online",
      "--status-connecting",
      "--status-offline",
    ];
    for (const token of nonTextTokens) {
      const fg = tokenMap.get(token)!;
      for (const bg of [...surfaces, tokenMap.get("--lavender")!]) {
        expect(
          contrastRatio(fg, bg),
          `${token} (${fg}) on ${bg}`,
        ).toBeGreaterThanOrEqual(3);
      }
    }
    const textLum = luminance(parseHex(tokenMap.get("--text")!)!);
    const mutedLum = luminance(parseHex(tokenMap.get("--text-muted")!)!);
    expect(
      Math.max(textLum, mutedLum) / Math.max(Math.min(textLum, mutedLum), 0.001),
    ).toBeGreaterThanOrEqual(1.55);
    for (let tone = 1; tone <= 6; tone++) {
      expect(
        contrastRatio(
          tokenMap.get(`--avatar-${tone}-fg`)!,
          tokenMap.get(`--avatar-${tone}-bg`)!,
        ),
        `avatar tone ${tone} contrast`,
      ).toBeGreaterThanOrEqual(4.5);
    }
    expect(
      contrastRatio(tokenMap.get("--brand")!, "#ffffff"),
    ).toBeGreaterThanOrEqual(4.5);
    for (const fill of ["--danger", "--danger-hover"]) {
      expect(
        contrastRatio(tokenMap.get("--on-danger")!, tokenMap.get(fill)!),
        `--on-danger on ${fill}`,
      ).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("enforces @layer blocks, token-only font-size declarations, standard font weights, and zero raw hex colors outside tokens.css", () => {
    for (const file of cssFiles) {
      const content = readFileSync(join(srcDir, file), "utf8");
      expect(content, `${file} must declare @layer`).toMatch(/@layer\s+\w+/);
      for (const match of content.matchAll(/font-size\s*:\s*([^;}\n]+)/gi)) {
        const value = match[1].trim();
        const pxMatch = /^(\d+(?:\.\d+)?)px/.exec(value);
        if (pxMatch) {
          const px = Number(pxMatch[1]);
          expect(px, `${file}: font-size ${value}`).toBeGreaterThanOrEqual(12);
          expect(
            Number.isInteger(px),
            `${file}: fractional font-size ${value}`,
          ).toBe(true);
        }
        if (file !== "tokens.css") {
          expect(
            value.startsWith("var(--font-") ||
              value.startsWith("clamp(") ||
              value === "inherit",
            `${file}: raw font-size "${value}" must use var(--font-*) or clamp(...)`,
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
      expect(
        [...content.matchAll(/#[0-9a-fA-F]{3,8}\b/g)].map((m) => m[0]),
        `${file} should contain zero raw hex color literals`,
      ).toEqual([]);
      expect(
        [...content.matchAll(/%23[0-9a-fA-F]{3,8}\b/g)].map((m) => m[0]),
        `${file} should contain zero URL-encoded %23 hex color literals`,
      ).toEqual([]);
      expect(
        [...content.matchAll(/\b(?:rgba?|hsla?)\s*\(/gi)].map((m) => m[0]),
        `${file} should contain zero raw rgb()/hsl() color functions`,
      ).toEqual([]);
    }
  });

  it("caps --font-xs to <=25 badge/counter declarations, enforces >=85% --space-* coverage, --z-* tokenization, and 3-tier breakpoints", () => {
    const spacingPropRe =
      /^\s*(?:padding(?:-(?:top|right|bottom|left|inline|block))?|margin(?:-(?:top|right|bottom|left|inline|block))?|gap|row-gap|column-gap)\s*:\s*([^;]+);/;
    let fontXsCount = 0;
    let spacingTotal = 0;
    let spacingTokenized = 0;
    const rawZIndexes: string[] = [];
    const invalidBreakpoints: string[] = [];

    for (const file of cssFiles) {
      if (file === "tokens.css") continue;
      const content = readFileSync(join(srcDir, file), "utf8");
      fontXsCount += [...content.matchAll(/var\(--font-xs\)/g)].length;

      for (const m of content.matchAll(
        /@media[^{]*\((?:min|max)-width\s*:\s*(\d+)px\)/g,
      )) {
        const bp = Number(m[1]);
        if (![600, 780, 1250].includes(bp)) {
          invalidBreakpoints.push(`${file}: ${m[0]}`);
        }
      }

      const allLines = content.split("\n");
      // Skip lines 1-50 of styles/marketplace.css owned by concurrent PR #137.
      const lines =
        file === "styles/marketplace.css" ? allLines.slice(50) : allLines;

      lines.forEach((line, idx) => {
        const zMatch = /\bz-index\s*:\s*([^;}\n]+)/.exec(line);
        if (zMatch) {
          const zVal = zMatch[1].trim();
          if (
            !zVal.startsWith("var(--z-") &&
            !["0", "1", "-1", "-2", "auto"].includes(zVal)
          ) {
            rawZIndexes.push(`${file}:${idx + 1}: z-index: ${zVal}`);
          }
        }

        const spMatch = spacingPropRe.exec(line);
        if (!spMatch) return;
        const val = spMatch[1].trim();
        if (/(?:calc|max|min|clamp|env)\s*\(/.test(val)) return;
        for (const tok of val.split(/\s+/).filter(Boolean)) {
          if (
            tok === "0" ||
            tok === "0px" ||
            tok === "auto" ||
            tok === "inherit" ||
            tok === "initial" ||
            tok.endsWith("%") ||
            /\d+(?:vh|vw|dvh|dvw|em|rem)$/.test(tok)
          ) {
            continue;
          }
          spacingTotal++;
          if (tok.startsWith("var(--space-")) {
            spacingTokenized++;
          }
        }
      });
    }

    expect(fontXsCount).toBeLessThanOrEqual(25);
    expect(spacingTokenized / spacingTotal).toBeGreaterThanOrEqual(0.85);
    expect(rawZIndexes).toEqual([]);
    expect(invalidBreakpoints).toEqual([]);
  });
});

describe("PR-5 CSS cleanup, unified focus, icon scale & self-hosted DM Sans", () => {
  it("contains zero unreferenced CSS classes across all modular and feature stylesheets", () => {
    const allFiles = listRelativeFiles(srcDir);
    const tsFiles = allFiles.filter(
      (f) => f.endsWith(".ts") || f.endsWith(".tsx"),
    );
    let corpus =
      readFileSync(join(srcDir, "..", "index.html"), "utf8") + "\n";
    for (const f of tsFiles) {
      corpus += readFileSync(join(srcDir, f), "utf8") + "\n";
    }
    for (const cssFile of cssFiles) {
      if (cssFile === "tokens.css") continue;
      const content = readFileSync(join(srcDir, cssFile), "utf8");
      const classes = new Set<string>();
      for (const m of content.matchAll(/\.([a-zA-Z_-][a-zA-Z0-9_-]*)/g)) {
        classes.add(m[1]);
      }
      const unreferenced = [...classes].filter((cls) => !corpus.includes(cls));
      expect(unreferenced, `${cssFile} unreferenced classes`).toEqual([]);
    }
  });

  it("enforces bidirectional CSS/TSX className contract, strict <=700-line modular size bounds across CSS/TS/TSX, and bans fragile unclassed container selectors", () => {
    const barrelCss = readFileSync(join(srcDir, "styles.css"), "utf8");
    for (const modFile of modularStyleFiles) {
      expect(barrelCss).toContain(`@import "./${modFile}";`);
    }
    const barrelLayoutCss = readFileSync(join(srcDir, "layout.css"), "utf8");
    for (const modLayout of modularLayoutFiles) {
      expect(barrelLayoutCss).toContain(`@import "./${modLayout}";`);
    }

    const definedClasses = new Set<string>();
    for (const cssFile of cssFiles) {
      const content = readFileSync(join(srcDir, cssFile), "utf8");
      const lineCount = content.split("\n").length;
      expect(
        lineCount,
        `${cssFile} (${lineCount} lines) exceeds modular 700-line ceiling`,
      ).toBeLessThanOrEqual(700);
      expect(
        content,
        `${cssFile} must not use fragile unclassed > div structural selectors on member/agent rows`,
      ).not.toMatch(/\.member-row\s*>\s*div\b|\.agent-detail-heading\s*>\s*div\b/);
      for (const m of content.matchAll(/\.([a-zA-Z_-][a-zA-Z0-9_-]*)/g)) {
        definedClasses.add(m[1]);
      }
    }

    const allFiles = listRelativeFiles(srcDir);
    const nonTestSourceFiles = allFiles.filter(
      (f) =>
        (f.endsWith(".ts") || f.endsWith(".tsx")) &&
        !f.endsWith(".test.ts") &&
        !f.endsWith(".test.tsx"),
    );
    for (const file of nonTestSourceFiles) {
      const lineCount = readFileSync(join(srcDir, file), "utf8").split(
        "\n",
      ).length;
      expect(
        lineCount,
        `${file} (${lineCount} lines) exceeds modular 700-line ceiling`,
      ).toBeLessThanOrEqual(700);
    }

    const nonTestTsx = nonTestSourceFiles.filter((f) => f.endsWith(".tsx"));
    const undefinedTokens: string[] = [];
    for (const file of nonTestTsx) {
      const content = readFileSync(join(srcDir, file), "utf8");
      for (const m of content.matchAll(/className="([^"{}$]+)"/g)) {
        for (const token of m[1].trim().split(/\s+/)) {
          if (token && !definedClasses.has(token)) {
            undefinedTokens.push(`${file}: "${token}"`);
          }
        }
      }
      for (const m of content.matchAll(/className=\{`([^`]+)`\}/g)) {
        const stripped = m[1].replace(/\$\{[^}]+\}/g, " ");
        for (const token of stripped.trim().split(/\s+/)) {
          if (token && !token.endsWith("-") && !definedClasses.has(token)) {
            undefinedTokens.push(`${file}: "${token}"`);
          }
        }
      }
    }
    expect(undefinedTokens, "TSX className tokens missing CSS rules").toEqual(
      [],
    );
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
      // --focus-ring is a full outline shorthand; nesting it in another
      // shorthand makes the declaration invalid and drops the ring.
      expect(content, `${file} invalid nested focus ring`).not.toMatch(
        /outline\s*:[^;]*\S\s+var\(--focus-ring\)|outline\s*:\s*var\(--focus-ring\)\s+[^;\s]/,
      );
      for (const match of content.matchAll(
        /:focus(?:-visible|-within)?[^{]*\{[^}]*outline\s*:\s*([^;]+);/g,
      )) {
        const value = match[1].trim();
        expect(
          value === "none" || value.startsWith("var(--focus-ring"),
          `${file}: focus outline should use the --focus-ring token, got "${value}"`,
        ).toBe(true);
      }
      expect(content, `${file} should have no !important`).not.toContain(
        "!important",
      );

      // Ban outline: none|0 inside @layer features, and require any file using
      // outline: none|0 in base/layout/components to pair it with :focus-visible or :focus-within.
      const featureBlocks: string[] = [];
      const layerRe = /@layer\s+([a-z-]+)\s*\{/g;
      let lm: RegExpExecArray | null;
      while ((lm = layerRe.exec(content))) {
        let depth = 1;
        let idx = lm.index + lm[0].length;
        while (idx < content.length && depth > 0) {
          if (content[idx] === "{") depth++;
          else if (content[idx] === "}") depth--;
          idx++;
        }
        if (lm[1] === "features") {
          featureBlocks.push(content.slice(lm.index + lm[0].length, idx - 1));
        }
      }
      for (const block of featureBlocks) {
        expect(
          block,
          `${file} must not declare outline: none/0 inside @layer features`,
        ).not.toMatch(/outline\s*:\s*(none|0)\b/);
      }
      if (/outline\s*:\s*(none|0)\b/.test(content)) {
        expect(
          /:focus-(visible|within)/.test(content),
          `${file} declares outline: none/0 without a paired :focus-visible or :focus-within rule`,
        ).toBe(true);
      }
    }
  });

  it("standardizes Lucide icon sizes onto ICON scale {14, 16, 18, 20, 24} with no raw numeric size expressions", () => {
    expect(ICON).toEqual({
      xs: 14,
      sm: 16,
      md: 18,
      lg: 20,
      xl: 24,
    });
    const tsxFiles = listRelativeFiles(srcDir).filter(
      (f) => f.endsWith(".tsx") && !f.endsWith(".test.tsx"),
    );
    for (const file of tsxFiles) {
      const content = readFileSync(join(srcDir, file), "utf8");
      for (const match of content.matchAll(/size=\{([^}]+)\}/g)) {
        const expr = match[1].trim();
        expect(
          /\b\d+\b/.test(expr),
          `${file}: raw numeric icon size expression size={${expr}}`,
        ).toBe(false);
        expect(
          expr.includes("ICON"),
          `${file}: icon size={${expr}} must use ICON scale`,
        ).toBe(true);
      }
    }
  });

  it("enforces @teamie/api-client <=700-line module bounds, zero frontend import cycles (SCCs > 1), and root CSS consolidation", () => {
    const apiClientSrcDir = join(srcDir, "../../../packages/api-client/src");
    const apiClientFiles = listRelativeFiles(apiClientSrcDir).filter(
      (f) => f.endsWith(".ts") && f !== "generated.ts",
    );
    expect(apiClientFiles.length).toBeGreaterThanOrEqual(4);
    for (const file of apiClientFiles) {
      const lineCount = readFileSync(join(apiClientSrcDir, file), "utf8").split(
        "\n",
      ).length;
      expect(
        lineCount,
        `packages/api-client/src/${file} (${lineCount} lines) exceeds modular 500-line ceiling`,
      ).toBeLessThanOrEqual(500);
    }

    const rootCssFiles = readdirSync(srcDir)
      .filter((entry) => entry.endsWith(".css"))
      .sort();
    expect(rootCssFiles).toEqual([
      "layers.css",
      "layout.css",
      "styles.css",
      "tokens.css",
    ]);
    const nonStylesCss = listRelativeFiles(srcDir).filter(
      (f) =>
        f.endsWith(".css") &&
        !rootCssFiles.includes(f) &&
        !f.startsWith("styles/"),
    );
    expect(nonStylesCss).toEqual([]);

    const nonTestSourceFiles = listRelativeFiles(srcDir).filter(
      (f) =>
        (f.endsWith(".ts") || f.endsWith(".tsx")) &&
        !f.endsWith(".test.ts") &&
        !f.endsWith(".test.tsx"),
    );
    const fullPathSet = new Set(
      nonTestSourceFiles.map((f) => join(srcDir, f)),
    );
    const resolveRelativeImport = (
      fromFile: string,
      spec: string,
    ): string | null => {
      if (!spec.startsWith(".")) return null;
      const base = join(dirname(fromFile), spec);
      for (const ext of ["", ".ts", ".tsx", "/index.ts", "/index.tsx"]) {
        if (fullPathSet.has(base + ext)) return base + ext;
      }
      return null;
    };
    const adjacency = new Map<string, string[]>();
    for (const rel of nonTestSourceFiles) {
      const full = join(srcDir, rel);
      const content = readFileSync(full, "utf8");
      const deps = new Set<string>();
      for (const m of content.matchAll(
        /(?:import|export)\s+(?:type\s+)?(?:[\s\S]*?\s+from\s+)?["'](\.[^"']+)["']/g,
      )) {
        const target = resolveRelativeImport(full, m[1]);
        if (target && target !== full) deps.add(target);
      }
      adjacency.set(full, [...deps]);
    }

    let index = 0;
    const indices = new Map<string, number>();
    const lowlink = new Map<string, number>();
    const onStack = new Set<string>();
    const stack: string[] = [];
    const circularSccs: string[][] = [];
    const strongConnect = (v: string) => {
      indices.set(v, index);
      lowlink.set(v, index);
      index++;
      stack.push(v);
      onStack.add(v);
      for (const w of adjacency.get(v) || []) {
        if (!indices.has(w)) {
          strongConnect(w);
          lowlink.set(v, Math.min(lowlink.get(v)!, lowlink.get(w)!));
        } else if (onStack.has(w)) {
          lowlink.set(v, Math.min(lowlink.get(v)!, indices.get(w)!));
        }
      }
      if (lowlink.get(v) === indices.get(v)) {
        const scc: string[] = [];
        let w: string;
        do {
          w = stack.pop()!;
          onStack.delete(w);
          scc.push(relative(srcDir, w).replace(/\\/g, "/"));
        } while (w !== v);
        if (scc.length > 1) circularSccs.push(scc.sort());
      }
    };
    for (const full of fullPathSet) {
      if (!indices.has(full)) strongConnect(full);
    }
    expect(circularSccs, "circular relative import SCCs").toEqual([]);
  });
});
