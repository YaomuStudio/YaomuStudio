// 扁平字典的复数选择（无新依赖）。
//
// 约定：数量文案使用扁平后缀键 `<key>.one` / `<key>.other`（可按需增加
// zero/two/few/many）。当前语言有可用 family（至少 `.other`）时按该语言
// 的 Intl.PluralRules 选类别；否则回退英文并重新按英文规则计算，而不是
// 拿当前语言的类别去查英文。`count` 是唯一数量来源，params 不能覆盖。

export type PluralMessages = Record<string, string>;
export type CountParams = Record<string, string | number>;

/** 无效数量不伪装成 0，也不抛错导致白屏。 */
export const INVALID_COUNT_TEXT = "—";

export function isValidCount(count: unknown): count is number {
  return typeof count === "number" && Number.isSafeInteger(count) && count >= 0;
}

const pluralRulesCache = new Map<string, Intl.PluralRules | null>();

function pluralRulesFor(locale: string): Intl.PluralRules | null {
  if (pluralRulesCache.has(locale)) return pluralRulesCache.get(locale) ?? null;
  try {
    const rules = new Intl.PluralRules(locale);
    pluralRulesCache.set(locale, rules);
    return rules;
  } catch {
    pluralRulesCache.set(locale, null);
    return null;
  }
}

export function pluralCategory(locale: string, count: number): string {
  return pluralRulesFor(locale)?.select(count) ?? "other";
}

/** 该字典是否为给定 key 提供可用 family（至少 `.other` 非空）。 */
export function hasPluralFamily(messages: PluralMessages | undefined, key: string): boolean {
  if (!messages) return false;
  const other = messages[`${key}.other`];
  return typeof other === "string" && other.trim() !== "";
}

function familyTemplate(
  messages: PluralMessages | undefined,
  key: string,
  count: number,
  locale: string,
): string | undefined {
  if (!hasPluralFamily(messages, key) || !messages) return undefined;
  const category = pluralCategory(locale, count);
  const exact = messages[`${key}.${category}`];
  if (typeof exact === "string" && exact.trim() !== "") return exact;
  return messages[`${key}.other`];
}

/**
 * 当前语言 family → 英文 family（英文规则重算）→ undefined（由调用方回退 key）。
 */
export function resolvePluralTemplate(
  current: PluralMessages | undefined,
  english: PluralMessages,
  locale: string,
  key: string,
  count: number,
): string | undefined {
  return (
    familyTemplate(current, key, count, locale) ??
    familyTemplate(english, key, count, "en")
  );
}

/** 替换 `{name}` 占位；未提供的占位保持原样。 */
export function interpolateMessage(template: string, params?: CountParams): string {
  if (!params) return template;
  let text = template;
  for (const [name, value] of Object.entries(params)) {
    text = text.split(`{${name}}`).join(String(value));
  }
  return text;
}

/** 数量参数由 count 唯一决定，params.count 不生效。 */
export function formatCountMessage(
  template: string,
  count: number,
  params?: CountParams,
): string {
  return interpolateMessage(template, { ...params, count });
}
