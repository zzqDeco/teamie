export const MARKETPLACE_SLUG_PATTERN = /^[a-z0-9][a-z0-9_-]{0,63}$/;

export const MARKETPLACE_SLUG_HELP =
  "1–64 位小写字母、数字、下划线（_）或连字符（-），且以字母或数字开头；连字符在 Agent 工具名中会转为下划线（mcp__<slug>__<tool>）。";

export const MARKETPLACE_DESCRIPTION_MAX_LENGTH = 500;

export function validateMarketplaceSlug(raw: string): string | null {
  const normalized = raw.trim().toLowerCase();
  if (!MARKETPLACE_SLUG_PATTERN.test(normalized)) {
    return "唯一标识（Slug）需为 1–64 位小写字母、数字、下划线（_）或连字符（-），且以字母或数字开头。";
  }
  return null;
}

export function sanitizeMcpToolSegment(raw: string): string {
  const out = raw
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]/g, "_")
    .replace(/^_+|_+$/g, "");
  return out || "tool";
}

export function reviewStatusMeta(
  status?: string,
  adminSuspended?: boolean,
): {
  label: string;
  className: string;
} {
  if (adminSuspended || status === "suspended") {
    return { label: "已封禁", className: "mp-badge-rejected" };
  }
  switch (status) {
    case "pending_review":
      return { label: "审核中", className: "mp-badge-pending" };
    case "approved":
      return { label: "已上架", className: "mp-badge-approved" };
    case "rejected":
      return { label: "已驳回", className: "mp-badge-rejected" };
    default:
      return { label: "草稿", className: "mp-badge-draft" };
  }
}

export function reviewActionLabel(action: string): string {
  switch (action) {
    case "submit":
    case "submitted":
      return "开发者提交审核";
    case "withdraw":
    case "withdrawn":
      return "开发者撤回提审";
    case "verify":
    case "verified":
      return "协议联调检测";
    case "approve":
    case "approved":
      return "管理员审批上架";
    case "reject":
    case "rejected":
      return "管理员驳回提审";
    case "suspend":
    case "suspended":
      return "管理员封禁下架";
    case "unsuspend":
    case "unsuspended":
      return "管理员解除封禁";
    default:
      return action;
  }
}

export function probeStageLabel(stage: string): string {
  switch (stage) {
    case "url_security":
      return "阶段 1 · URL 格式、跨源与安全策略检测";
    case "web_ui":
      return "阶段 2 · Web UI 在线可达与 iframe 嵌入头检测";
    case "mcp_auth":
      return "阶段 3 · MCP 鉴权门禁检测（拦截无凭证与伪造签名）";
    case "mcp_handshake":
      return "阶段 4 · MCP 真实签名握手与工具发现";
    default:
      return stage;
  }
}
