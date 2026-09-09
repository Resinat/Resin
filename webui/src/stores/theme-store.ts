import { create } from "zustand";

type ThemeMode = "system" | "light" | "dark";
type ResolvedTheme = "light" | "dark";

const STORAGE_KEY = "resin_theme";

function resolveTheme(mode: ThemeMode): ResolvedTheme {
  if (mode === "light" || mode === "dark") return mode;
  if (typeof window === "undefined") return "light";
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function applyTheme(resolved: ResolvedTheme): void {
  if (typeof document !== "undefined") {
    document.documentElement.setAttribute("data-theme", resolved);
  }
}

function loadInitialMode(): ThemeMode {
  if (typeof window === "undefined") return "system";
  const stored = window.localStorage.getItem(STORAGE_KEY);
  if (stored === "light" || stored === "dark") return stored;
  return "system";
}

type ThemeState = {
  mode: ThemeMode;
  resolved: ResolvedTheme;
  setMode: (mode: ThemeMode) => void;
  cycleMode: () => void;
};

const initialMode = loadInitialMode();
const initialResolved = resolveTheme(initialMode);
applyTheme(initialResolved);

export const useThemeStore = create<ThemeState>((set, get) => ({
  mode: initialMode,
  resolved: initialResolved,
  setMode: (mode) => {
    const resolved = resolveTheme(mode);
    if (typeof window !== "undefined") {
      if (mode === "system") {
        window.localStorage.removeItem(STORAGE_KEY);
      } else {
        window.localStorage.setItem(STORAGE_KEY, mode);
      }
    }
    applyTheme(resolved);
    set({ mode, resolved });
  },
  cycleMode: () => {
    const order: ThemeMode[] = ["system", "light", "dark"];
    const current = get().mode;
    const next = order[(order.indexOf(current) + 1) % order.length];
    get().setMode(next);
  },
}));

// Listen for system theme changes when in "system" mode
if (typeof window !== "undefined") {
  const mql = window.matchMedia("(prefers-color-scheme: dark)");
  mql.addEventListener("change", () => {
    const { mode, setMode } = useThemeStore.getState();
    if (mode === "system") {
      setMode("system"); // re-resolve
    }
  });
}
