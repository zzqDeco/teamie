import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { BREAKPOINTS, MAX_WIDTH_QUERY } from "./breakpoints";
import { formatDateTime } from "./format-datetime";

const srcDir = dirname(fileURLToPath(import.meta.url));

function listNonTestSourceFiles(dir: string, root = dir): string[] {
  const results: string[] = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      results.push(...listNonTestSourceFiles(full, root));
    } else {
      const rel = relative(root, full).replace(/\\/g, "/");
      if (
        (rel.endsWith(".ts") || rel.endsWith(".tsx")) &&
        !rel.endsWith(".test.ts") &&
        !rel.endsWith(".test.tsx")
      ) {
        results.push(rel);
      }
    }
  }
  return results.sort();
}

/**
 * Public marketing / brand hero files may refer to human "团队" in prose
 * (e.g. "团队与 Agent"), whereas authenticated `/app` UI must use "工作空间"
 * for the Workspace entity and "成员" for participants.
 */
const PUBLIC_MARKETING_TEAM_PROSE_FILES = new Set([
  "public.tsx",
  "public/collaboration-story.tsx",
  "public/auth-views.tsx",
  "password-recovery.tsx",
]);

describe("PR-5 global terminology, breakpoints & date/time i18n guardrails", () => {
  it("converges product terminology onto 工作空间 / 私信 / 小组 / 频道 / 应用 across non-test sources", () => {
    const violations: string[] = [];
    for (const file of listNonTestSourceFiles(srcDir)) {
      const lines = readFileSync(join(srcDir, file), "utf8").split("\n");
      lines.forEach((line, index) => {
        const lineNo = index + 1;
        // Explicit allowlist exception: lines 1-140 of marketplace/conversation-apps.tsx
        // belong to concurrent PR #137 (feat/web-conversation-apps-resize) and are untouched
        // until #137 merges.
        if (file === "marketplace/conversation-apps.tsx" && lineNo <= 140) {
          return;
        }
        // Strip single-line and block comments before checking user-facing strings.
        const codeOnly = line.replace(/\/\/.*$/, "");
        if (/私聊|群聊|工作区/.test(codeOnly)) {
          violations.push(`${file}:${lineNo}: ${line.trim()}`);
        }
        if (
          !PUBLIC_MARKETING_TEAM_PROSE_FILES.has(file) &&
          /团队/.test(codeOnly)
        ) {
          violations.push(`${file}:${lineNo}: ${line.trim()}`);
        }
      });
    }
    expect(violations).toEqual([]);
  });

  it("centralizes all date/time formatting in format-datetime.ts with zero raw toLocale*String calls elsewhere", () => {
    const violations: string[] = [];
    for (const file of listNonTestSourceFiles(srcDir)) {
      if (file === "format-datetime.ts") continue;
      const lines = readFileSync(join(srcDir, file), "utf8").split("\n");
      lines.forEach((line, index) => {
        if (/\.toLocale(?:Date|Time)?String\s*\(/.test(line)) {
          violations.push(`${file}:${index + 1}: ${line.trim()}`);
        }
      });
    }
    expect(violations).toEqual([]);

    const now = new Date("2026-11-12T10:35:00+08:00");
    expect(
      formatDateTime("2026-11-02T10:35:00+08:00", "stamp", now),
    ).toContain("11月2日");
    expect(
      formatDateTime("2026-11-11T09:15:00+08:00", "day", now),
    ).toBe("昨天");
    expect(
      formatDateTime("2025-12-31T23:59:00+08:00", "stamp", now),
    ).toContain("2025年");
  });

  it("exports shared 3-tier viewport breakpoints (600 / 780 / 1250)", () => {
    expect(BREAKPOINTS).toEqual({ sm: 600, md: 780, lg: 1250 });
    expect(MAX_WIDTH_QUERY).toEqual({
      sm: "(max-width: 600px)",
      md: "(max-width: 780px)",
      lg: "(max-width: 1250px)",
    });
  });
});
