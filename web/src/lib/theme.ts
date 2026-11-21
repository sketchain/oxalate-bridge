export type ThemeChoice = "system" | "light" | "dark";
const KEY = "ob.theme";

export function getTheme(): ThemeChoice {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "light" || v === "dark") return v;
  } catch {
    // storage unavailable
  }
  return "system";
}

function systemDark(): boolean {
  return typeof window !== "undefined" && window.matchMedia("(prefers-color-scheme: dark)").matches;
}

export function isDark(choice: ThemeChoice): boolean {
  return choice === "dark" || (choice === "system" && systemDark());
}

export function applyTheme(choice: ThemeChoice) {
  document.documentElement.classList.toggle("dark", isDark(choice));
}

export function setTheme(choice: ThemeChoice) {
  try {
    if (choice === "system") localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, choice);
  } catch {
    // storage unavailable: the choice lasts for this page only
  }
  applyTheme(choice);
  window.dispatchEvent(new CustomEvent("ob-theme", { detail: choice }));
}

/** Follows the system setting while the choice is "system". */
export function watchSystemTheme(): () => void {
  const mq = window.matchMedia("(prefers-color-scheme: dark)");
  const fn = () => {
    if (getTheme() === "system") applyTheme("system");
  };
  mq.addEventListener("change", fn);
  return () => mq.removeEventListener("change", fn);
}
