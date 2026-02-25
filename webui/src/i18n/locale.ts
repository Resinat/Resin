export type AppLocale = "zh-CN" | "en-US";

export const STORAGE_KEY = "resin.webui.locale";
export const DEFAULT_LOCALE: AppLocale = "zh-CN";
export const SUPPORTED_LOCALES: readonly AppLocale[] = ["zh-CN", "en-US"];

let currentLocale: AppLocale = DEFAULT_LOCALE;

function isLocale(value: unknown): value is AppLocale {
  return value === "zh-CN" || value === "en-US";
}

export function normalizeLocale(value: string | null | undefined): AppLocale {
  if (value && isLocale(value)) {
    return value;
  }
  if (value?.toLowerCase().startsWith("zh")) {
    return "zh-CN";
  }
  return "en-US";
}

export function detectInitialLocale(): AppLocale {
  if (typeof window === "undefined") {
    return DEFAULT_LOCALE;
  }

  const stored = window.localStorage.getItem(STORAGE_KEY);
  if (isLocale(stored)) {
    return stored;
  }

  return normalizeLocale(window.navigator.language);
}

export function persistLocale(locale: AppLocale) {
  if (typeof window === "undefined") {
    return;
  }
  window.localStorage.setItem(STORAGE_KEY, locale);
}

export function getCurrentLocale(): AppLocale {
  return currentLocale;
}

export function setCurrentLocale(locale: AppLocale) {
  currentLocale = locale;
}

export function isEnglishLocale(locale: AppLocale): boolean {
  return locale === "en-US";
}
