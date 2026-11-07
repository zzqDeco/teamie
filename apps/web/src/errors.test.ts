import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { ApiError } from "@teamie/api-client";
import { errorMessages, errorText } from "./errors";

const srcDir = dirname(fileURLToPath(import.meta.url));
const marketplaceErrorsGo = join(
  srcDir,
  "../../../services/marketplace/internal/domain/errors.go",
);
const GENERIC = "请求失败，请稍后重试。";

function marketplaceDomainCodes() {
  const source = readFileSync(marketplaceErrorsGo, "utf8");
  const codes = new Map<string, number>();
  for (const match of source.matchAll(/Err\((\d{3}),\s*"([a-z0-9_]+)"/g))
    codes.set(match[2], Number(match[1]));
  return codes;
}

describe("error copy", () => {
  it("maps every Marketplace domain error code to actionable Chinese copy", () => {
    const codes = marketplaceDomainCodes();
    // Guard the parser itself so a refactor of errors.go cannot pass vacuously.
    expect(codes.size).toBeGreaterThanOrEqual(20);
    for (const code of [
      "duplicate_slug",
      "invalid_slug",
      "same_origin_web_url",
      "https_required",
      "forbidden_endpoint_host",
      "developer_app_limit_reached",
      "slug_immutable",
      "revision_conflict",
      "endpoint_verification_failed",
      "verify_rate_limited",
    ])
      expect(codes.has(code), code).toBe(true);
    const unmapped = [...codes.keys()].filter(
      (code) => !Object.hasOwn(errorMessages, code),
    );
    expect(unmapped).toEqual([]);
    for (const [code, status] of codes) {
      const text = errorText(new ApiError(status, code, "English server text"));
      expect(text, code).not.toBe(GENERIC);
      expect(text, code).toMatch(/[\u4e00-\u9fff]/);
      expect(text, code).not.toContain("English server text");
    }
  });

  it("describes feature flags as a platform switch, not a workspace setting", () => {
    const text = errorText(new ApiError(403, "feature_disabled", "off"));
    expect(text).toContain("平台");
    expect(text).not.toContain("工作空间管理员");
  });

  it("keeps the shared invalid_name wording generic for non-Marketplace forms", () => {
    expect(errorText(new ApiError(400, "invalid_name", "bad"))).toBe(
      "名称格式不正确，请修改后重试。",
    );
    expect(errorText(new ApiError(418, "unknown_code", "x"))).toBe(GENERIC);
    expect(errorText(new ApiError(400, "constructor", "x"))).toBe(GENERIC);
  });
});
