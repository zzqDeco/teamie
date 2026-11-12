export const BREAKPOINTS = {
  sm: 600,
  md: 780,
  lg: 1250,
} as const;

export type BreakpointKey = keyof typeof BREAKPOINTS;

export const MAX_WIDTH_QUERY = {
  sm: `(max-width: ${BREAKPOINTS.sm}px)`,
  md: `(max-width: ${BREAKPOINTS.md}px)`,
  lg: `(max-width: ${BREAKPOINTS.lg}px)`,
} as const;
