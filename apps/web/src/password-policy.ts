export const PASSWORD_MIN_CODE_POINTS = 12;
export const PASSWORD_MAX_UTF8_BYTES = 72;

/** Rule status using the server's semantics: Unicode code points and UTF-8 bytes. */
export function passwordRules(password: string) {
  return {
    minLength: Array.from(password).length >= PASSWORD_MIN_CODE_POINTS,
    maxBytes:
      new TextEncoder().encode(password).length <= PASSWORD_MAX_UTF8_BYTES,
  };
}

export function passwordPolicyError(password: string): string {
  const rules = passwordRules(password);
  if (!rules.minLength) return "密码至少需要 12 个字符。";
  if (!rules.maxBytes) return "密码过长，请控制在 72 字节以内。";
  return "";
}
