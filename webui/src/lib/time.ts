export function formatDateTime(input: string): string {
  if (!input) {
    return "-";
  }

  const time = new Date(input);
  if (Number.isNaN(time.getTime())) {
    return input;
  }

  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(time);
}
