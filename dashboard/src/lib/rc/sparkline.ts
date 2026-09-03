const MAX_POINTS = 24;

export function appendSparkline(
  history: Array<{ value: number }>,
  next: number | undefined,
): Array<{ value: number }> {
  if (next == null || Number.isNaN(next)) {
    return history;
  }
  return [...history, { value: next }].slice(-MAX_POINTS);
}
