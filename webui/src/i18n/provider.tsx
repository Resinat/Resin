import { useEffect, useMemo, useState, type ReactNode } from "react";
import { attachLegacyDomTranslation } from "./legacy-dom";
import { detectInitialLocale, isEnglishLocale, persistLocale, setCurrentLocale, type AppLocale } from "./locale";
import { translate, translateDocumentTitle } from "./translations";
import { I18nContext, type I18nContextValue } from "./context";

type I18nProviderProps = {
  children: ReactNode;
};

export function I18nProvider({ children }: I18nProviderProps) {
  const [locale, setLocale] = useState<AppLocale>(() => {
    const initial = detectInitialLocale();
    // Ensure formatter helpers read the correct locale during first render.
    setCurrentLocale(initial);
    return initial;
  });

  // Keep module-level locale synchronized during render for formatter helpers.
  setCurrentLocale(locale);

  useEffect(() => {
    persistLocale(locale);
    document.documentElement.lang = locale;
    document.title = translateDocumentTitle(locale);
  }, [locale]);

  useEffect(() => {
    return attachLegacyDomTranslation(locale);
  }, [locale]);

  const value = useMemo<I18nContextValue>(() => {
    return {
      locale,
      isEnglish: isEnglishLocale(locale),
      setLocale,
      t: (text: string) => translate(locale, text),
    };
  }, [locale, setLocale]);

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}
