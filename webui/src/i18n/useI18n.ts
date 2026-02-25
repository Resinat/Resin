import { useCallback, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { isEnglishLocale, normalizeLocale, type AppLocale } from "./locale";

export type I18nContextValue = {
  locale: AppLocale;
  isEnglish: boolean;
  setLocale: (locale: AppLocale) => void;
  t: (text: string, options?: Record<string, unknown>) => string;
};

export function useI18n(): I18nContextValue {
  const { t, i18n } = useTranslation();

  const locale = normalizeLocale(i18n.resolvedLanguage ?? i18n.language);
  const setLocale = useCallback(
    (next: AppLocale) => {
      void i18n.changeLanguage(next);
    },
    [i18n],
  );

  return useMemo<I18nContextValue>(
    () => ({
      locale,
      isEnglish: isEnglishLocale(locale),
      setLocale,
      t: (text, options) => t(text, options),
    }),
    [locale, setLocale, t],
  );
}
