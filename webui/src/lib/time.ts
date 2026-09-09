import { getCurrentLocale, isEnglishLocale } from "../i18n/locale";

function isEnglish(): boolean {
  return isEnglishLocale(getCurrentLocale());
}

export function formatDateTime(input: string): string {
  if (!input) {
    return "-";
  }

  const time = new Date(input);
  if (Number.isNaN(time.getTime())) {
    return input;
  }

  const locale = isEnglish() ? "en-US" : "zh-CN";

  return new Intl.DateTimeFormat(locale, {
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
  const english = isEnglish();
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
    return english ? "0s" : "0 秒";
  }

  const days = Math.floor(wholeSeconds / 86_400);
  const hours = Math.floor((wholeSeconds % 86_400) / 3_600);
  const minutes = Math.floor((wholeSeconds % 3_600) / 60);
  const seconds = wholeSeconds % 60;

  if (english) {
    if (days > 0) {
      return `${days}d ${hours}h`;
    }
    if (hours > 0) {
      return `${hours}h ${minutes}m`;
    }
    if (minutes > 0) {
      return `${minutes}m ${seconds}s`;
    }
    return `${seconds}s`;
  }

  if (days > 0) {
    return `${days} 天 ${hours} 小时`;
  }
  if (hours > 0) {
    return `${hours} 小时 ${minutes} 分钟`;
  }
  if (minutes > 0) {
    return `${minutes} 分钟 ${seconds} 秒`;
  }
  return `${seconds} 秒`;
}

export function formatRelativeTime(input: string | null | undefined, emptyLabel = "-"): string {
  const english = isEnglish();
  if (!input) {
    return emptyLabel;
  }

  const time = new Date(input);
  if (Number.isNaN(time.getTime())) {
    return String(input);
  }

  const now = new Date();
  const rawDiff = now.getTime() - time.getTime();
  const isFuture = rawDiff < 0;
  const diff = Math.abs(rawDiff);

  const seconds = Math.floor(diff / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);
  const months = Math.floor(days / 30);
  const years = Math.floor(days / 365);

  if (english) {
    if (years > 0) return isFuture ? `in ${years} year${years === 1 ? "" : "s"}` : `${years} year${years === 1 ? "" : "s"} ago`;
    if (months > 0) return isFuture ? `in ${months} month${months === 1 ? "" : "s"}` : `${months} month${months === 1 ? "" : "s"} ago`;
    if (days > 0) return isFuture ? `in ${days} day${days === 1 ? "" : "s"}` : `${days} day${days === 1 ? "" : "s"} ago`;
    if (hours > 0) return isFuture ? `in ${hours} hour${hours === 1 ? "" : "s"}` : `${hours} hour${hours === 1 ? "" : "s"} ago`;
    if (minutes > 0) return isFuture ? `in ${minutes} minute${minutes === 1 ? "" : "s"}` : `${minutes} minute${minutes === 1 ? "" : "s"} ago`;
    return "just now";
  }

  const suffix = isFuture ? "后" : "前";
  if (years > 0) return `${years} 年${suffix}`;
  if (months > 0) return `${months} 个月${suffix}`;
  if (days > 0) return `${days} 天${suffix}`;
  if (hours > 0) return `${hours} 小时${suffix}`;
  if (minutes > 0) return `${minutes} 分钟${suffix}`;
  return "刚刚";
}
