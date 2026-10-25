import i18n, { type TFunction } from "i18next";
import { initReactI18next } from "react-i18next";
import { en } from "./locales/en.ts";
import { ko } from "./locales/ko.ts";

export function resolveLanguage(
  preference: string | undefined,
  stored: string | null,
  languages: readonly string[],
): "en" | "ko" {
  if (preference === "en" || preference === "ko") return preference;
  if (preference !== "" && (stored === "en" || stored === "ko")) return stored;
  for (const language of languages) {
    const value = language.toLowerCase();
    if (value.startsWith("ko")) return "ko";
    if (value.startsWith("en")) return "en";
  }
  return "en";
}

function storedLanguage() {
  try {
    return localStorage.getItem("archika.language");
  } catch {
    return null;
  }
}
function browserLanguages() {
  return typeof navigator === "undefined" ? [] : navigator.languages;
}
export function applyLanguage(preference?: string) {
  const language = resolveLanguage(preference, storedLanguage(), browserLanguages());
  try {
    if (preference === "") localStorage.removeItem("archika.language");
    else if (preference === "en" || preference === "ko")
      localStorage.setItem("archika.language", preference);
  } catch {
    // Storage can be disabled by the browser.
  }
  void i18n.changeLanguage(language);
}
function syncLanguage(language: string) {
  if (typeof document !== "undefined")
    document.documentElement.lang = language === "ko" ? "ko" : "en";
}
i18n.on("languageChanged", syncLanguage);
void i18n.use(initReactI18next).init({
  resources: { en: { translation: en }, ko: { translation: ko } },
  lng: resolveLanguage(undefined, storedLanguage(), browserLanguages()),
  fallbackLng: "en",
  defaultNS: "translation",
  interpolation: { escapeValue: false },
});
syncLanguage(i18n.language);

declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "translation";
    resources: { translation: typeof en };
  }
}

export function statusLabel(value: string, t: TFunction = i18n.t) {
  return isStatus(value) ? t(`status.${value}`) : value;
}
function isStatus(value: string): value is keyof typeof en.status {
  return Object.hasOwn(en.status, value);
}
export function isErrorCode(value: string): value is keyof typeof ko.errors {
  return Object.hasOwn(ko.errors, value);
}
export function serverText(value: string, t: TFunction = i18n.t) {
  const key = Object.keys(en.server).find(
    (key) => isServerMessage(key) && en.server[key] === value,
  );
  return key && isServerMessage(key) ? t(`server.${key}`) : value;
}
function isServerMessage(value: string): value is keyof typeof en.server {
  return Object.hasOwn(en.server, value);
}
export default i18n;
