// One-shot reason shown on the login page after a signed-in session expires.
// The login page reads it during render and clears it after mount, so a later
// explicit visit does not keep claiming the session expired.
export const SESSION_EXPIRED_KEY = "teamie.sessionExpired";
export function markSessionExpired() {
  try {
    sessionStorage.setItem(SESSION_EXPIRED_KEY, "1");
  } catch {
    /* No storage access; the login page simply shows no reason. */
  }
}
export function readSessionExpired() {
  try {
    return sessionStorage.getItem(SESSION_EXPIRED_KEY) === "1";
  } catch {
    return false;
  }
}
export function clearSessionExpired() {
  try {
    sessionStorage.removeItem(SESSION_EXPIRED_KEY);
  } catch {
    /* No storage access. */
  }
}
