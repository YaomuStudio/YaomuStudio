// 记住每个功能最近一次「创建成功」的目标语言；按功能隔离，避免互相污染。
import { TARGET_LANGUAGES } from "@/domain/languages";

export type TargetLanguageFeature = "pipeline" | "custom" | "translate" | "voice-clone";

const KEY_PREFIX = "yaomu.targetLanguages.";
const VALID_CODES = new Set(TARGET_LANGUAGES.map((language) => language.code));

function storageKey(feature: TargetLanguageFeature): string {
  return `${KEY_PREFIX}${feature}`;
}

/** 读取该功能上次成功创建的目标语言（过滤未知/重复语言，旧数据安全降级）。 */
export function loadRecentTargetLanguages(feature: TargetLanguageFeature): string[] {
  try {
    const raw = localStorage.getItem(storageKey(feature));
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    const unique = new Set<string>();
    for (const value of parsed) {
      if (typeof value === "string" && VALID_CODES.has(value)) unique.add(value);
    }
    return [...unique];
  } catch {
    return [];
  }
}

/** 记录创建成功的目标语言；空列表时清除，避免下次误带空偏好。 */
export function rememberTargetLanguages(
  feature: TargetLanguageFeature,
  languages: string[],
): void {
  const unique = [...new Set(languages.filter((code) => VALID_CODES.has(code)))];
  try {
    if (unique.length === 0) localStorage.removeItem(storageKey(feature));
    else localStorage.setItem(storageKey(feature), JSON.stringify(unique));
  } catch {
    // 存储不可用只影响下次预填，不阻塞创建。
  }
}
