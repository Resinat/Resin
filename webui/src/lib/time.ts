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

export function formatGoDuration(input: string, emptyLabel = "-"): string {
  const raw = input.trim();
  if (!raw) {
    return emptyLabel;
  }

  const pattern = /(\d+(?:\.\d+)?)(h|m|s)/g;
  let totalSeconds = 0;
  let consumedLength = 0;
  let match: RegExpExecArray | null;

  while ((match = pattern.exec(raw)) !== null) {
    const value = Number(match[1]);
    if (Number.isNaN(value)) {
      return raw;
    }

    consumedLength += match[0].length;
    if (match[2] === "h") {
      totalSeconds += value * 3600;
    } else if (match[2] === "m") {
      totalSeconds += value * 60;
    } else {
      totalSeconds += value;
    }
  }

  if (!consumedLength || consumedLength !== raw.length) {
    return raw;
  }

  const wholeSeconds = Math.floor(totalSeconds);
  if (wholeSeconds <= 0) {
    return "0秒";
  }

  const days = Math.floor(wholeSeconds / 86_400);
  const hours = Math.floor((wholeSeconds % 86_400) / 3_600);
  const minutes = Math.floor((wholeSeconds % 3_600) / 60);
  const seconds = wholeSeconds % 60;

  const parts: string[] = [];
  if (days > 0) {
    parts.push(`${days}天`);
  }
  if (hours > 0) {
    parts.push(`${hours}小时`);
  }
  if (days === 0 && minutes > 0) {
    parts.push(`${minutes}分钟`);
  }
  if (days === 0 && hours === 0 && seconds > 0) {
    parts.push(`${seconds}秒`);
  }

  return parts.slice(0, 2).join("");
}

export function formatRelativeTime(input: string | null | undefined, emptyLabel = "-"): string {
  if (!input) {
    return emptyLabel;
  }

  const time = new Date(input);
  if (Number.isNaN(time.getTime())) {
    return String(input);
  }

  const now = new Date();
  const diff = Math.max(0, now.getTime() - time.getTime());

  const seconds = Math.floor(diff / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);
  const months = Math.floor(days / 30);
  const years = Math.floor(days / 365);

  if (years > 0) return `${years} 年前`;
  if (months > 0) return `${months} 个月前`;
  if (days > 0) return `${days} 天前`;
  if (hours > 0) return `${hours} 小时前`;
  if (minutes > 0) return `${minutes} 分钟前`;
  return "刚刚";
}
