import { ApiError } from "@teamie/api-client";

export function isNetworkError(error: unknown) {
  return (
    error instanceof TypeError ||
    (error instanceof Error &&
      /failed to fetch|load failed|networkerror/i.test(error.message))
  );
}

/**
 * Actionable copy for Marketplace codes. Together with the shared codes in
 * `errorMessages` this covers every code in
 * services/marketplace/internal/domain/errors.go (errors.test.ts enforces it).
 * Shared codes (`forbidden`, `feature_disabled`, `invalid_name`, `not_found`)
 * keep their generic wording because other services use them too.
 */
export const marketplaceMessages: Record<string, string> = {
  invalid_slug:
    "唯一标识（Slug）需为 1 至 64 个小写字母、数字、连字符或下划线，请修改后重试。",
  duplicate_slug: "这个唯一标识（Slug）已被其他应用使用，请换一个。",
  invalid_description: "应用简介最多 500 个字符，请精简后重试。",
  invalid_url: "请填写有效的 http:// 或 https:// Web UI 与 MCP Server 地址。",
  invalid_mcp_url:
    "MCP Server 地址需为有效的 http(s) 地址，且不能包含账号密码或 # 片段。",
  same_origin_web_url:
    "Web UI 必须部署在独立域名上，不能与 Teamie 同源，请更换地址。",
  https_required: "生产环境的应用地址必须使用 https://，请更换为 HTTPS 地址。",
  forbidden_endpoint_host:
    "应用地址必须指向公网可访问的主机，不能使用内网、本机或保留地址。",
  marketplace_group_only: "应用只能在小组中使用。",
  human_developer_required: "只有人类账户可以管理开发者应用。",
  developer_app_limit_reached:
    "每个账户最多创建 20 个应用，请先删除不再需要的草稿。",
  slug_immutable: "应用提交审核或上架后，唯一标识（Slug）不能再修改。",
  marketplace_app_review_pending: "应用正在审核中，请先撤回提审再修改草稿。",
  invalid_review_state: "应用当前的审核状态不允许此操作，请刷新后查看最新状态。",
  revision_conflict: "提审内容已更新，请刷新后审核最新提交。",
  marketplace_app_suspended: "该应用已被平台管理员封禁，暂时无法修改或使用。",
  invalid_review_note: "审核备注最多 1000 个字符，请精简后重试。",
  review_note_required: "驳回或封禁应用时，请填写明确的原因。",
  endpoint_verification_failed:
    "线上服务未通过协议检测，请查看检测报告中未通过的项目，修复后重新检测。",
  verify_rate_limited: "检测过于频繁，请等待几秒后再试。",
  unauthenticated: "登录已失效，请重新登录。",
  invalid_token: "应用会话已失效，请重新打开应用。",
  token_expired: "应用会话已过期，请重新打开应用。",
  chat_unavailable: "服务暂时不可用，请稍后重试。",
  identity_unavailable: "服务暂时不可用，请稍后重试。",
};

export const errorMessages: Record<string, string> = {
  ...marketplaceMessages,
  invalid_credentials: "邮箱或密码不正确，请重新输入。",
  invalid_current_password: "当前密码不正确，请重新输入。",
  invalid_password: "密码需至少 12 个字符，且不超过 72 字节。",
  password_unchanged: "新密码不能与当前密码相同。",
  unauthorized: "登录已失效，请重新登录。",
  invalid_invite: "邀请码无效、已使用或已失效，请联系管理员获取新的邀请。",
  invalid_invitation:
    "邀请码无效、已使用或已失效，请联系管理员获取新的邀请。",
  forbidden: "你当前没有执行此操作的权限。",
  not_found: "内容不存在或已无法访问。",
  conflict: "这条消息与之前的发送记录冲突，请刷新后重试。",
  idempotency_conflict: "这条消息与之前的发送记录冲突，请删除后重新发送。",
  // Feature flags are platform-level backend switches, not workspace settings.
  feature_disabled: "该功能已由平台关闭，暂时无法使用。",
  rate_limited: "操作过于频繁，请稍后再试。",
  attachment_busy: "文件传输繁忙，请稍后重试。",
  attachment_capacity: "待发送文件过多，请移除部分文件后重试。",
  attachment_too_large: "文件不能超过 4 MiB。",
  too_many_attachments: "单条消息最多可附加 8 个文件。",
  invalid_attachment: "文件已过期或不可用，请重新上传。",
  invalid_filename: "文件名无效，请重命名后上传。",
  invalid_upload: "上传文件格式无效，请重新选择文件。",
  invalid_upload_key: "上传标识无效，请重新上传文件。",
  invalid_body: "消息内容需在 1 到 8000 个字符之间。",
  invalid_mentions: "提及的成员不在当前对话中或格式无效，请修改后重新发送。",
  invalid_reply: "引用回复的目标消息不在当前对话中，请移除引用后重新发送。",
  forward_cross_workspace: "仅支持在同一工作空间内转发消息。",
  invalid_forward: "无法转发该消息，原消息可能已被撤回或无法访问。",
  pin_limit: "当前对话最多只能置顶 50 条消息，请先取消置顶部分旧消息。",
  saved_limit: "个人收藏数量已达上限（1000 条），请先清理不再需要的收藏。",
  broadcast_forbidden: "当前对话不允许使用全体提及，或仅工作空间所有者可在频道中使用。",
  presence_unavailable: "在线状态已关闭，当前无法使用 @here 提及在线成员。",
  invalid_client_message_id: "消息标识无效，请删除后重新发送。",
  message_retracted: "已撤回的消息无法继续操作。",
  reaction_limit: "单条消息最多可添加 20 种不同表情回应。",
  email_exists: "此邮箱已注册，请直接登录。",
  email_in_use: "此邮箱已注册，请直接登录。",
  invalid_email: "邮箱格式不正确，请检查后重试。",
  invalid_name: "名称格式不正确，请修改后重试。",
  invalid_request: "请求内容无效，请检查后重试。",
  invalid_query: "搜索关键词无效，请调整后重试（最多 200 个字符）。",
  upload_conflict: "这个文件与之前的上传记录冲突，请重新选择文件。",
  attachment_upload_unavailable: "文件已过期或已被移除，请重新上传。",
  attachment_upload_not_found: "没有找到这次上传，请重新上传。",
  invalid_reset_link: "重置链接无效或已过期，请联系管理员重新生成。",
  session_changed: "登录状态已变化，请刷新页面后重试。",
};

export function errorText(error: unknown) {
  if (!(error instanceof ApiError)) {
    if (error instanceof DOMException && error.name === "AbortError")
      return "请求已取消";
    if (isNetworkError(error)) return "网络连接失败，请检查网络后重试。";
    return "请求失败，请稍后重试。";
  }
  if (Object.hasOwn(errorMessages, error.code)) return errorMessages[error.code];
  if (error.status >= 500) return "服务暂时不可用，请稍后重试。";
  if (error.status === 404) return "内容不存在或已无法访问。";
  return "请求失败，请稍后重试。";
}
