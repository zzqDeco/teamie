/**
 * The single date/time formatter for chat surfaces. Every label goes through
 * `Intl.DateTimeFormat` (cached per pattern) so the locale lives in one place
 * and relative days (今天/昨天) and cross-year dates are handled consistently.
 */
export const DATE_LOCALE = "zh-CN";

export type DateTimeKind =
  /** 10:35 */
  | "time"
  /** Date separator: 今天 / 昨天 / 11月2日 星期一 / 2025年11月2日 星期日 */
  | "day"
  /** Compact list label: 今天 10:35 / 昨天 10:35 / 11月2日 10:35 / 2025年11月2日 10:35 */
  | "datetime"
  /** Short date: 11月2日 / 2025年11月2日 */
  | "date"
  /** Unambiguous tooltip: 2026年11月2日 星期一 10:35 */
  | "full";

const formatters = new Map<string, Intl.DateTimeFormat>();
function formatter(options: Intl.DateTimeFormatOptions) {
  const key = JSON.stringify(options);
  let cached = formatters.get(key);
  if (!cached) {
    cached = new Intl.DateTimeFormat(DATE_LOCALE, options);
    formatters.set(key, cached);
  }
  return cached;
}

const TIME: Intl.DateTimeFormatOptions = {
  hour: "2-digit",
  minute: "2-digit",
  hourCycle: "h23",
};

function startOfDay(value: Date) {
  return new Date(value.getFullYear(), value.getMonth(), value.getDate()).getTime();
}

/** Whole local calendar days between `value` and `now` (0 = today, 1 = yesterday). */
export function calendarDaysAgo(value: Date, now: Date) {
  return Math.round((startOfDay(now) - startOfDay(value)) / 86_400_000);
}

export function sameLocalDay(a: string | Date, b: string | Date) {
  const left = new Date(a);
  const right = new Date(b);
  return (
    left.getFullYear() === right.getFullYear() &&
    left.getMonth() === right.getMonth() &&
    left.getDate() === right.getDate()
  );
}

function monthDay(value: Date, now: Date, weekday: boolean) {
  return formatter({
    ...(value.getFullYear() !== now.getFullYear() ? { year: "numeric" } : {}),
    month: "long",
    day: "numeric",
    ...(weekday ? { weekday: "long" } : {}),
  }).format(value);
}

export function formatDateTime(
  input: string | number | Date,
  kind: DateTimeKind,
  now: Date = new Date(),
): string {
  const value = new Date(input);
  if (Number.isNaN(value.getTime())) return "";
  switch (kind) {
    case "time":
      return formatter(TIME).format(value);
    case "full":
      return formatter({
        year: "numeric",
        month: "long",
        day: "numeric",
        weekday: "long",
        ...TIME,
      }).format(value);
    case "date":
      return monthDay(value, now, false);
    case "day":
    case "datetime": {
      const days = calendarDaysAgo(value, now);
      const relative = days === 0 ? "今天" : days === 1 ? "昨天" : "";
      if (kind === "day") return relative || monthDay(value, now, true);
      return `${relative || monthDay(value, now, false)} ${formatter(TIME).format(value)}`;
    }
  }
}
