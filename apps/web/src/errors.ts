import { ApiError } from "@teamie/api-client";

export function isNetworkError(error: unknown) {
  return (
    error instanceof TypeError ||
    (error instanceof Error &&
      /failed to fetch|load failed|networkerror/i.test(error.message))
  );
}

export function errorText(error: unknown) {
  if (!(error instanceof ApiError)) {
    if (error instanceof DOMException && error.name === "AbortError")
      return "请求已取消";
    if (isNetworkError(error)) return "网络连接失败，请检查网络后重试。";
    return "请求失败，请稍后重试。";
  }
  const messages: Record<string, string> = {
    invalid_credentials: "邮箱或密码不正确，请重新输入。",
    invalid_current_password: "当前密码不正确，请重新输入。",
    invalid_password: "密码需至少 12 个字符，且不超过 72 字节。",
    password_unchanged: "新密码不能与当前密码相同。",
    unauthorized: "登录已失效，请重新登录。",
    invalid_invite: "邀请码无效、已使用或已失效，请联系管理员获取新的邀请。",
    invalid_invitation:
      "邀请码无效、已使用或已失效，请联系管理员获取新的邀请。",
    forbidden: "你当前没有执行此操作的权限。",
    conflict: "这条消息与之前的发送记录冲突，请刷新后重试。",
    idempotency_conflict: "这条消息与之前的发送记录冲突，请删除后重新发送。",
    feature_disabled: "此功能已由工作空间管理员关闭。",
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
  if (messages[error.code]) return messages[error.code];
  if (error.status >= 500) return "服务暂时不可用，请稍后重试。";
  if (error.status === 404) return "内容不存在或已无法访问。";
  return "请求失败，请稍后重试。";
}
