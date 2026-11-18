// 语言包有效条目与完成度（同一口径）。
//
// 有效条目 = 英文已知键 + 非空字符串 + 占位参数与英文一致。
// 特例：英文已知复数 family（存在非空 `.other`）下，允许按 CLDR 需要追加
// zero/two/few/many 类别；这些条目用该 family 的 `.other` 校验占位。
// 英文键本身是无可见文本的分隔符（如空格）时，本地化的空白/空字符串也有效。
// 未知键、未知 family、乱后缀、其他空白值、非字符串、占位错误都不进入运行时
// 字典，也不计入完成度；缺键仍由 t() 回退英文。
// 完成度只统计与英文键集合的交集，且不计与英文完全相同的值；额外类别可用于渲染但不增加英文覆盖数。

export type PackMessages = Record<string, string>;

const placeholderPattern = /\{([A-Za-z0-9_]+)\}/g;

/** 允许按 CLDR 复数规则追加的类别；one/other 走英文精确键校验。 */
const EXTRA_PLURAL_CATEGORIES = new Set(["zero", "two", "few", "many"]);

function splitCategorySuffix(key: string): { base: string; category: string } | null {
  const dot = key.lastIndexOf(".");
  if (dot <= 0 || dot === key.length - 1) return null;
  return { base: key.slice(0, dot), category: key.slice(dot + 1) };
}

function placeholders(value: string): string {
  return [...value.matchAll(placeholderPattern)]
    .map((match) => match[1])
    .sort()
    .join(",");
}

export function isNonEmptyMessage(value: unknown): value is string {
  return typeof value === "string" && value.trim() !== "";
}

/** 英文本身没有可见文本时，允许本地化保留空格/空分隔符。 */
function isBlankSeparatorAllowed(key: string, value: string, english: PackMessages): boolean {
  return (
    Object.prototype.hasOwnProperty.call(english, key) &&
    value.trim() === "" &&
    (english[key] ?? "").trim() === ""
  );
}

/** 过滤非法条目；不修改入参。 */
export function sanitizeLocalePack(raw: unknown, english: PackMessages): PackMessages {
  const sanitized: PackMessages = {};
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return sanitized;
  for (const [key, value] of Object.entries(raw as Record<string, unknown>)) {
    if (!isNonEmptyMessage(value)) {
      if (typeof value === "string" && isBlankSeparatorAllowed(key, value, english)) {
        sanitized[key] = value;
      }
      continue;
    }
    let englishValue = english[key];
    // 英文本身是分隔符（如空格）时，允许本地化用有意义的单位替换。
    if (
      Object.prototype.hasOwnProperty.call(english, key) &&
      typeof englishValue === "string" &&
      englishValue.trim() === ""
    ) {
      if (placeholders(value) === "") sanitized[key] = value;
      continue;
    }
    if (!isNonEmptyMessage(englishValue)) {
      // 额外复数类别：只有英文已知 family（合法 .other）才可用其 .other 校验。
      const split = splitCategorySuffix(key);
      if (!split || !EXTRA_PLURAL_CATEGORIES.has(split.category)) continue;
      const familyFallback = english[`${split.base}.other`];
      if (!isNonEmptyMessage(familyFallback)) continue;
      englishValue = familyFallback;
    }
    if (placeholders(value) !== placeholders(englishValue)) continue;
    sanitized[key] = value;
  }
  return sanitized;
}

export function validMessageCount(messages: PackMessages): number {
  let count = 0;
  for (const value of Object.values(messages)) {
    if (isNonEmptyMessage(value)) count += 1;
  }
  return count;
}

/**
 * 已翻译英文键覆盖 / 有效英文键总数；与英文完全相同的值不计为已翻译。
 * 额外复数类别不占英文覆盖数，封顶 100；无英文键或无有效交集为 0。
 */
export function completionPercent(pack: PackMessages, english: PackMessages): number {
  const englishCount = validMessageCount(english);
  if (englishCount === 0) return 0;
  let covered = 0;
  for (const [key, value] of Object.entries(pack)) {
    if (!isNonEmptyMessage(value)) continue;
    if (!Object.prototype.hasOwnProperty.call(english, key)) continue;
    const englishValue = english[key];
    if (!isNonEmptyMessage(englishValue)) continue;
    if (placeholders(value) !== placeholders(englishValue)) continue;
    // 英文 fallback 不应把“完成度”抬成 100%。
    if (value === englishValue) continue;
    covered += 1;
  }
  if (covered === 0) return 0;
  return Math.min(100, Math.round((covered / englishCount) * 100));
}
