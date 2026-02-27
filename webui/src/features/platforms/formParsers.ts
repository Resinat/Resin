export function parseLinesToList(input: string | undefined, normalize?: (value: string) => string): string[] {
  if (!input) {
    return [];
  }

  return input
    .split(/\n/)
    .map((item) => item.trim())
    .filter(Boolean)
    .map((item) => (normalize ? normalize(item) : item));
}

export function parseHeaderLines(input: string | undefined): string[] {
  const lines = parseLinesToList(input);
  const seen = new Set<string>();
  const headers: string[] = [];
  for (const line of lines) {
    const key = line.toLowerCase();
    if (seen.has(key)) {
      continue;
    }
    seen.add(key);
    headers.push(line);
  }
  return headers;
}
