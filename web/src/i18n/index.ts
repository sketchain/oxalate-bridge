import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import zhCN from "./locales/zh-CN.json";
import en from "./locales/en.json";

export const LANGUAGES = ["zh-CN", "en"] as const;
export type Language = (typeof LANGUAGES)[number];
const KEY = "ob.lang";

function initial(): Language {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "en" || v === "zh-CN") return v;
  } catch {
    // ignore
  }
  return "zh-CN";
}

void i18n.use(initReactI18next).init({
  resources: { "zh-CN": { translation: zhCN }, en: { translation: en } },
  lng: initial(),
  fallbackLng: "zh-CN",
  interpolation: { escapeValue: false },
  returnNull: false,
});

i18n.on("languageChanged", (lng) => {
  document.documentElement.lang = lng;
});
document.documentElement.lang = i18n.language;

export function setLanguage(lng: Language) {
  try {
    localStorage.setItem(KEY, lng);
  } catch {
    // ignore
  }
  void i18n.changeLanguage(lng);
}

export default i18n;
