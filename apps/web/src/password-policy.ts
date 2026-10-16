export function passwordPolicyError(password: string): string {
  if (Array.from(password).length < 12) return "密码至少需要 12 个字符。";
  if (new TextEncoder().encode(password).length > 72)
    return "密码过长，请控制在 72 字节以内。";
  return "";
}
