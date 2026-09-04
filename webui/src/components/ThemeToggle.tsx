import { Monitor, Moon, Sun } from "lucide-react";
import { cn } from "../lib/cn";
import { useThemeStore } from "../stores/theme-store";
import { useI18n } from "../i18n";

type ThemeToggleProps = {
  className?: string;
};

export function ThemeToggle({ className }: ThemeToggleProps) {
  const { t } = useI18n();
  const mode = useThemeStore((s) => s.mode);
  const cycleMode = useThemeStore((s) => s.cycleMode);

  const icon =
    mode === "light" ? <Sun size={14} /> : mode === "dark" ? <Moon size={14} /> : <Monitor size={14} />;

  const label = mode === "light" ? t("浅色") : mode === "dark" ? t("深色") : t("系统");

  return (
    <button
      type="button"
      className={cn("locale-switch-compact", className)}
      onClick={cycleMode}
      aria-label={t("切换主题")}
      title={t("切换主题")}
    >
      {icon}
      <span>{label}</span>
    </button>
  );
}
