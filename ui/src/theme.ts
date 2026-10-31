// Theme: follows prefers-color-scheme unless the viewer picked light or dark.
// The choice lives in localStorage; every access is guarded because storage can
// throw (private windows, blocked site data) and the app must work without it.
export type ThemeChoice = "system" | "light" | "dark";

const KEY = "skillci.theme";

export function readTheme(): ThemeChoice {
  try {
    const v = window.localStorage.getItem(KEY);
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}

export function applyTheme(choice: ThemeChoice): void {
  const root = document.documentElement;
  if (choice === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", choice);
}

export function saveTheme(choice: ThemeChoice): void {
  applyTheme(choice);
  try {
    if (choice === "system") window.localStorage.removeItem(KEY);
    else window.localStorage.setItem(KEY, choice);
  } catch {
    // The choice still applies to this page view.
  }
}

/** nextTheme cycles system, light, dark. */
export function nextTheme(choice: ThemeChoice): ThemeChoice {
  return choice === "system" ? "light" : choice === "light" ? "dark" : "system";
}
