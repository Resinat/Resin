import { Languages } from "lucide-react";
import { cn } from "../lib/cn";
import { useI18n } from "../i18n";

type LanguageSwitcherProps = {
  className?: string;
  compact?: boolean;
};

export function LanguageSwitcher({ className, compact = false }: LanguageSwitcherProps) {
  const { locale, setLocale, t } = useI18n();

  if (compact) {
    const nextLocale = locale === "zh-CN" ? "en-US" : "zh-CN";
    return (
      <button
        type="button"
        className={cn("locale-switch-compact", className)}
        onClick={() => setLocale(nextLocale)}
        aria-label={t("切换语言")}
        title={t("切换语言")}
      >
        <Languages size={14} />
        <span>{locale === "zh-CN" ? "中" : "EN"}</span>
      </button>
    );
  }

  return (
    <div className={cn("locale-switch", className)} role="group" aria-label={t("切换语言")}>
      <button
        type="button"
        className={cn("locale-switch-btn", locale === "zh-CN" && "locale-switch-btn-active")}
        onClick={() => setLocale("zh-CN")}
        title="中文"
      >
        中文
      </button>
      <button
        type="button"
        className={cn("locale-switch-btn", locale === "en-US" && "locale-switch-btn-active")}
        onClick={() => setLocale("en-US")}
        title="English"
      >
        EN
      </button>
    </div>
  );
}
