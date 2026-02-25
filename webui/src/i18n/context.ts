import { createContext } from "react";
import type { AppLocale } from "./locale";

export type I18nContextValue = {
  locale: AppLocale;
  isEnglish: boolean;
  setLocale: (locale: AppLocale) => void;
  t: (text: string) => string;
};

export const I18nContext = createContext<I18nContextValue | null>(null);
