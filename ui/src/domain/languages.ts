// 语言清单：UI 语言、ASR 源语言、OCR 语言、译制目标语言。
// code 口径对齐 docs/business-parity.md §6；未决项（zh-tw/tl、方言→zh）不在此冻结。

import type { Language } from "./types";

/** 26 种 UI 语言（FR-SET-01/02）。语言包完成度由 i18n 运行时按实际资源计算，不在此硬编码。 */
export interface UiLanguage extends Language {
  rtl: boolean;
}

export const UI_LANGUAGES: UiLanguage[] = [
  { code: "zh-CN", name: "简体中文", nativeName: "简体中文", rtl: false },
  { code: "en", name: "English", nativeName: "English", rtl: false },
  { code: "zh-TW", name: "繁體中文", nativeName: "繁體中文", rtl: false },
  { code: "ja", name: "日本語", nativeName: "日本語", rtl: false },
  { code: "ko", name: "한국어", nativeName: "한국어", rtl: false },
  { code: "ru", name: "Русский", nativeName: "Русский", rtl: false },
  { code: "fr", name: "Français", nativeName: "Français", rtl: false },
  { code: "de", name: "Deutsch", nativeName: "Deutsch", rtl: false },
  { code: "es", name: "Español", nativeName: "Español", rtl: false },
  { code: "pt", name: "Português", nativeName: "Português", rtl: false },
  { code: "ar", name: "العربية", nativeName: "العربية", rtl: true },
  { code: "he", name: "עברית", nativeName: "עברית", rtl: true },
  { code: "fa", name: "فارسی", nativeName: "فارسی", rtl: true },
  { code: "it", name: "Italiano", nativeName: "Italiano", rtl: false },
  { code: "hi", name: "हिन्दी", nativeName: "हिन्दी", rtl: false },
  { code: "id", name: "Bahasa Indonesia", nativeName: "Bahasa Indonesia", rtl: false },
  { code: "th", name: "ไทย", nativeName: "ไทย", rtl: false },
  { code: "tr", name: "Türkçe", nativeName: "Türkçe", rtl: false },
  { code: "uk", name: "Українська", nativeName: "Українська", rtl: false },
  { code: "vi", name: "Tiếng Việt", nativeName: "Tiếng Việt", rtl: false },
  { code: "cs", name: "Čeština", nativeName: "Čeština", rtl: false },
  { code: "da", name: "Dansk", nativeName: "Dansk", rtl: false },
  { code: "fi", name: "Suomi", nativeName: "Suomi", rtl: false },
  { code: "ms", name: "Bahasa Melayu", nativeName: "Bahasa Melayu", rtl: false },
  { code: "no", name: "Norsk", nativeName: "Norsk", rtl: false },
  { code: "pl", name: "Polski", nativeName: "Polski", rtl: false },
];

/** ASR 源语言：31 种（ISO 639-1 + 中文方言）。方言提交统一映射 zh。 */
export interface AsrLanguage extends Language {
  /** 提交给后端的 code；方言映射为 zh。 */
  submitCode: string;
  dialect?: boolean;
}

export const ASR_LANGUAGES: AsrLanguage[] = [
  { code: "zh", name: "简体中文", nativeName: "简体中文", submitCode: "zh" },
  { code: "zh-tw", name: "繁体中文", nativeName: "繁體中文", submitCode: "zh-tw" },
  { code: "en", name: "英语", nativeName: "English", submitCode: "en" },
  { code: "ja", name: "日语", nativeName: "日本語", submitCode: "ja" },
  { code: "de", name: "德语", nativeName: "Deutsch", submitCode: "de" },
  { code: "ko", name: "韩语", nativeName: "한국어", submitCode: "ko" },
  { code: "ru", name: "俄语", nativeName: "Русский", submitCode: "ru" },
  { code: "fr", name: "法语", nativeName: "Français", submitCode: "fr" },
  { code: "pt", name: "葡萄牙语", nativeName: "Português", submitCode: "pt" },
  { code: "ar", name: "阿拉伯语", nativeName: "العربية", submitCode: "ar" },
  { code: "it", name: "意大利语", nativeName: "Italiano", submitCode: "it" },
  { code: "es", name: "西班牙语", nativeName: "Español", submitCode: "es" },
  { code: "hi", name: "印地语", nativeName: "हिन्दी", submitCode: "hi" },
  { code: "id", name: "印尼语", nativeName: "Bahasa Indonesia", submitCode: "id" },
  { code: "th", name: "泰语", nativeName: "ไทย", submitCode: "th" },
  { code: "tr", name: "土耳其语", nativeName: "Türkçe", submitCode: "tr" },
  { code: "uk", name: "乌克兰语", nativeName: "Українська", submitCode: "uk" },
  { code: "vi", name: "越南语", nativeName: "Tiếng Việt", submitCode: "vi" },
  { code: "cs", name: "捷克语", nativeName: "Čeština", submitCode: "cs" },
  { code: "da", name: "丹麦语", nativeName: "Dansk", submitCode: "da" },
  { code: "tl", name: "菲律宾语", nativeName: "Filipino", submitCode: "tl" },
  { code: "fi", name: "芬兰语", nativeName: "Suomi", submitCode: "fi" },
  { code: "is", name: "冰岛语", nativeName: "Íslenska", submitCode: "is" },
  { code: "ms", name: "马来语", nativeName: "Bahasa Melayu", submitCode: "ms" },
  { code: "no", name: "挪威语", nativeName: "Norsk", submitCode: "no" },
  { code: "pl", name: "波兰语", nativeName: "Polski", submitCode: "pl" },
  { code: "sv", name: "瑞典语", nativeName: "Svenska", submitCode: "sv" },
  // 中文方言：统一按 zh 提交（FR-TOOL-04）
  { code: "zh-yue", name: "粤语", nativeName: "粤语", submitCode: "zh", dialect: true },
  { code: "zh-minnan", name: "闽南语", nativeName: "闽南语", submitCode: "zh", dialect: true },
  { code: "zh-wu", name: "吴语", nativeName: "吴语", submitCode: "zh", dialect: true },
  { code: "zh-sichuan", name: "四川话", nativeName: "四川话", submitCode: "zh", dialect: true },
];

/** OCR 源语言：仅简中/繁中/英/日（FR-TOOL-04）。 */
export const OCR_LANGUAGES: Language[] = [
  { code: "chinese", name: "简体中文", nativeName: "简体中文" },
  { code: "chinese_traditional", name: "繁體中文", nativeName: "繁體中文" },
  { code: "english", name: "English", nativeName: "English" },
  { code: "japanese", name: "日本語", nativeName: "日本語" },
];

/**
 * 译制/配音目标语言（多选）。清单与传参 code 对齐网页端（mls）支持语言接口：
 * ISO 639-1 小写；繁中/台湾 zh-tw、巴西葡语 pt-br、墨西哥西语 es-mx、粤语 chinese-粤语。
 * 名称固定中文 + 本地化名，与 UI 界面语言（UI_LANGUAGES）是两套口径，勿混用。
 */
export interface TargetLanguage extends Language {
  /** 首屏常用语言（其余折叠进“更多语言”）。 */
  popular?: boolean;
}

export const TARGET_LANGUAGES: TargetLanguage[] = [
  { code: "en", name: "英语", nativeName: "English", popular: true },
  { code: "es", name: "西班牙语", nativeName: "Español", popular: true },
  { code: "fr", name: "法语", nativeName: "Français", popular: true },
  { code: "de", name: "德语", nativeName: "Deutsch", popular: true },
  { code: "it", name: "意大利语", nativeName: "Italiano", popular: true },
  { code: "ru", name: "俄语", nativeName: "Русский", popular: true },
  { code: "pt-br", name: "巴西葡萄牙语", nativeName: "Português (Brasil)", popular: true },
  { code: "es-mx", name: "墨西哥西班牙语", nativeName: "Español (México)", popular: true },
  { code: "zh-tw", name: "普通话（台湾）", nativeName: "普通话（台湾）", popular: true },
  { code: "chinese-粤语", name: "粤语", nativeName: "粤语", popular: true },
  // 桌面端兼容：简体中文目标码沿用现有翻译/核心链路；mls 目标表未单列。
  { code: "zh-CN", name: "简体中文", nativeName: "简体中文", popular: true },
  { code: "ja", name: "日语", nativeName: "日本語", popular: true },
  { code: "ko", name: "韩语", nativeName: "한국어", popular: true },
  { code: "ar", name: "阿拉伯语", nativeName: "العربية" },
  { code: "hi", name: "印地语", nativeName: "हिन्दी" },
  { code: "th", name: "泰语", nativeName: "ไทย" },
  { code: "vi", name: "越南语", nativeName: "Tiếng Việt" },
  { code: "id", name: "印尼语", nativeName: "Bahasa Indonesia" },
  { code: "ms", name: "马来语", nativeName: "Bahasa Melayu" },
  { code: "tl", name: "菲律宾语", nativeName: "Filipino" },
  { code: "pl", name: "波兰语", nativeName: "Polski" },
  { code: "nl", name: "荷兰语", nativeName: "Nederlands" },
  { code: "tr", name: "土耳其语", nativeName: "Türkçe" },
  { code: "uk", name: "乌克兰语", nativeName: "Українська" },
  { code: "sv", name: "瑞典语", nativeName: "Svenska" },
  { code: "da", name: "丹麦语", nativeName: "Dansk" },
  { code: "fi", name: "芬兰语", nativeName: "Suomi" },
  { code: "no", name: "挪威语", nativeName: "Norsk" },
  { code: "cs", name: "捷克语", nativeName: "Čeština" },
  { code: "ro", name: "罗马尼亚语", nativeName: "Română" },
  { code: "hu", name: "匈牙利语", nativeName: "Magyar" },
  { code: "el", name: "希腊语", nativeName: "Ελληνικά" },
  { code: "he", name: "希伯来语", nativeName: "עברית" },
  { code: "bg", name: "保加利亚语", nativeName: "Български" },
  { code: "hr", name: "克罗地亚语", nativeName: "Hrvatski" },
  { code: "sw", name: "斯瓦希里语", nativeName: "Kiswahili" },
  { code: "kk", name: "哈萨克语", nativeName: "Қазақша" },
  { code: "uz", name: "乌兹别克语", nativeName: "Oʻzbekcha" },
];

/**
 * 目标语言显示名：中文界面显示中文名，其他界面显示本地名。
 * locale 由调用方从 i18n 传入，避免 domain → i18n 的循环依赖。
 */
export const targetLanguageLabel = (
  language: Pick<Language, "name" | "nativeName">,
  locale: string,
): string => {
  if (locale.toLowerCase().startsWith("zh")) return language.name;
  return language.nativeName || language.name;
};

/** code → 显示名（目标语言口径）。 */
export const targetLanguageName = (code: string, locale: string): string => {
  const language = TARGET_LANGUAGES.find((l) => l.code === code);
  return language ? targetLanguageLabel(language, locale) : code;
};

export const languageName = (code: string, list: Language[]): string =>
  list.find((l) => l.code === code)?.name ?? code;
