export type AppLocale = "zh-CN" | "en-US";

const STORAGE_KEY = "resin.webui.locale";
const DEFAULT_LOCALE: AppLocale = "zh-CN";

let currentLocale: AppLocale = DEFAULT_LOCALE;

function isLocale(value: unknown): value is AppLocale {
  return value === "zh-CN" || value === "en-US";
}

export function detectInitialLocale(): AppLocale {
  if (typeof window === "undefined") {
    return DEFAULT_LOCALE;
  }

  const stored = window.localStorage.getItem(STORAGE_KEY);
  if (isLocale(stored)) {
    return stored;
  }

  const browserLanguage = window.navigator.language.toLowerCase();
  if (browserLanguage.startsWith("zh")) {
    return "zh-CN";
  }

  return "en-US";
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
