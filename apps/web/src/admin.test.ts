import { describe, expect, it } from "vitest";
import { registrationInviteStatus } from "./admin";
import type { RegistrationInvite } from "@teamie/api-client";
const now = Date.parse("2026-10-16T07:00:00Z");
const invite = (
  changes: Partial<RegistrationInvite> = {},
): RegistrationInvite => ({
  id: "invite-1",
  createdAt: "2026-10-15T07:00:00Z",
  expiresAt: "2026-10-22T07:00:00Z",
  ...changes,
});
describe("single-use registration invitation availability", () => {
  it("expires an unused invitation at its exact expiry boundary", () => {
    expect(registrationInviteStatus(invite(), now)).toBe("available");
    expect(
      registrationInviteStatus(
        invite({ expiresAt: "2026-10-16T07:00:00Z" }),
        now,
      ),
    ).toBe("expired");
  });
  it("never makes a consumed invitation available again or offers revocation", () => {
    expect(
      registrationInviteStatus(invite({ usedAt: "2026-10-15T08:00:00Z" }), now),
    ).toBe("used");
    expect(
      registrationInviteStatus(
        invite({
          usedAt: "2026-10-15T08:00:00Z",
          revokedAt: "2026-10-15T09:00:00Z",
          expiresAt: "2026-10-15T10:00:00Z",
        }),
        now,
      ),
    ).toBe("used");
  });
  it("retains the revoked state after the invitation also expires", () => {
    expect(
      registrationInviteStatus(
        invite({
          revokedAt: "2026-10-15T08:00:00Z",
          expiresAt: "2026-10-15T10:00:00Z",
        }),
        now,
      ),
    ).toBe("revoked");
  });
});
