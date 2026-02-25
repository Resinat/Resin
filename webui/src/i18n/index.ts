import i18next from "i18next";
import { initReactI18next } from "react-i18next";
import {
  DEFAULT_LOCALE,
  detectInitialLocale,
  normalizeLocale,
  persistLocale,
  setCurrentLocale,
} from "./locale";
import { buildZhTranslations, EN_TRANSLATIONS, translateDocumentTitle } from "./translations";

const initialLocale = detectInitialLocale();
setCurrentLocale(initialLocale);

void i18next.use(initReactI18next).init({
  lng: initialLocale,
  fallbackLng: DEFAULT_LOCALE,
  supportedLngs: ["zh-CN", "en-US"],
  resources: {
    "zh-CN": {
      translation: buildZhTranslations(),
    },
    "en-US": {
      translation: EN_TRANSLATIONS as Record<string, string>,
    },
  },
  interpolation: {
    escapeValue: false,
  },
  keySeparator: false,
  nsSeparator: false,
});

if (typeof document !== "undefined") {
  document.documentElement.lang = initialLocale;
  document.title = translateDocumentTitle(initialLocale);
}

i18next.on("languageChanged", (language) => {
  const locale = normalizeLocale(language);
  setCurrentLocale(locale);
  persistLocale(locale);
  if (typeof document !== "undefined") {
    document.documentElement.lang = locale;
    document.title = translateDocumentTitle(locale);
  }
});

export default i18next;
export { useI18n } from "./useI18n";
export { normalizeLocale };
