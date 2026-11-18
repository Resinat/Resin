export function readPageSize(key: string, options: readonly number[], fallback: number): number {
  try {
    const saved = Number(window.localStorage.getItem(`resin.webui.page-size.${key}`));
    return options.includes(saved) ? saved : fallback;
  } catch {
    return fallback;
  }
}

export function savePageSize(key: string, value: number): void {
  try {
    window.localStorage.setItem(`resin.webui.page-size.${key}`, String(value));
  } catch {
    // Pagination remains usable when browser storage is unavailable.
  }
}
