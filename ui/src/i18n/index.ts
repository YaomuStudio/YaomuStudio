// 轻量 i18n：中文/英文完整覆盖（FR-SET-02），其余语种由语言包承载，缺键回退英文。
// 文案键为扁平点分字符串，如 "pipeline.step1.title"。
// 各页面新增文案放入本页面目录 messages.ts，由 register.ts 汇总注册。

import { reactive, ref } from "vue";
import { UI_LANGUAGES } from "@/domain/languages";
import { completionPercent, sanitizeLocalePack } from "./pack";
import {
  formatCountMessage,
  interpolateMessage,
  INVALID_COUNT_TEXT,
  isValidCount,
  resolvePluralTemplate,
} from "./plural";

// 26 种 UI 语言 code（FR-SET-01）。除 zh-CN/en 外由语言包承载，缺键回退英文（FR-SET-02）。
export type Locale = string;
export type Messages = Record<string, string>;
export interface AreaMessages {
  en: Messages;
  zhCN: Messages;
}

/** RTL 界面语言；目前只设置文档方向，完整镜像布局未验证。 */
export const RTL_LOCALES: ReadonlySet<Locale> = new Set(["ar", "he", "fa"]);

/**
 * 系统语言就近匹配（FR-SET-01）：
 * 精确 code → 基础语言 → 中文变体（TW/HK/MO/Hant → zh-TW，其余 zh → zh-CN）→ en。
 * 首次启动无已保存语言时使用；语言设置页也复用同一规则。
 */
export function detectSystemLocale(): Locale {
  if (typeof navigator === "undefined") return "zh-CN";
  const code = (navigator.language || "en").toLowerCase();
  const exact = UI_LANGUAGES.find((language) => language.code.toLowerCase() === code);
  if (exact) return exact.code;
  const base = code.split("-")[0] ?? code;
  if (base === "zh") {
    return /(^|-)(tw|hk|mo|hant)(-|$)/.test(code) ? "zh-TW" : "zh-CN";
  }
  const partial = UI_LANGUAGES.find((language) => language.code.toLowerCase() === base);
  return partial?.code ?? "en";
}

const locale = ref<Locale>("zh-CN");

// 响应式字典：语言包异步加载完成后自动触发使用方重新渲染。
const dictionaries = reactive<Record<string, Messages>>({ "zh-CN": {}, en: {} });

// 语言包为独立 JSON，构建时按语言拆分 chunk，首次选择该语言时才加载。
const packLoaders = import.meta.glob<{ default: Messages }>("./locales/*.json");

type PackState = "loading" | "loaded" | "failed";

const packStates = new Map<Locale, PackState>();
const packTasks = new Map<Locale, Promise<void>>();

function packLoader(code: Locale): (() => Promise<{ default: Messages }>) | undefined {
  return packLoaders[`./locales/${code}.json`];
}

/** 是否存在该语言包资源（不代表已加载）。 */
export function hasLocalePack(code: Locale): boolean {
  return packLoader(code) !== undefined;
}

/**
 * 按需加载语言包。缺包/加载失败/解析失败都按空包处理并回退英文，不抛错。
 * 可重复调用：已加载或加载中直接复用。
 */
export function ensureLocalePack(code: Locale): Promise<void> {
  if (code === "zh-CN" || code === "en") return Promise.resolve();
  const state = packStates.get(code);
  if (state === "loaded" || state === "failed") return Promise.resolve();
  if (state === "loading") return packTasks.get(code) ?? Promise.resolve();

  const loader = packLoader(code);
  if (!loader) {
    // 没有资源是正常状态（尚未翻译），与加载失败同样回退英文。
    packStates.set(code, "failed");
    return Promise.resolve();
  }

  packStates.set(code, "loading");
  const task = loader()
    .then((mod) => {
      // 只接收英文已知键、非空字符串、占位一致的条目：
      // 未知/非法条目既不能显示，也不能抬高完成度。
      dictionaries[code] = sanitizeLocalePack(mod.default, dictionaries.en);
      packStates.set(code, "loaded");
    })
    .catch(() => {
      packStates.set(code, "failed");
      console.warn(`[i18n] language pack "${code}" failed to load; falling back to English.`);
    })
    .finally(() => {
      packTasks.delete(code);
    });
  packTasks.set(code, task);
  return task;
}

/**
 * 语言包完成度（FR-SET-02）：zh-CN/en 固定 100%；其余按有效包条目 / 有效英文键计算。
 * 未知键、空白值、非字符串、占位错误都不计入（与运行时 fallback 同一口径）。
 * 无包或空包为 0。只反映已加载的包，调用方需先 ensureLocalePack。
 */
export function getLocaleCompletion(code: Locale): number {
  if (code === "zh-CN" || code === "en") return 100;
  const pack = dictionaries[code];
  if (!pack) return 0;
  return completionPercent(pack, dictionaries.en);
}

export function registerMessages(area: AreaMessages): void {
  Object.assign(dictionaries.en, area.en);
  Object.assign(dictionaries["zh-CN"], area.zhCN);
}

/**
 * 英文词典只读快照（测试/校验用）：返回浅拷贝，调用方修改不会影响运行时字典。
 * 语言包测试用它校验“键集合 ⊆ 英文词典 + 占位一致”。
 */
export function getEnglishMessages(): Messages {
  return { ...dictionaries.en };
}

/** 应用界面语言：同步文档 lang/dir，并触发语言包加载。 */
export function setLocale(next: Locale): void {
  if (!next) return;
  locale.value = next;
  if (typeof document !== "undefined") {
    document.documentElement.lang = next;
    document.documentElement.dir = "ltr";
  }
  void ensureLocalePack(next);
}

export function getLocale(): Locale {
  return locale.value;
}

export function useLocale(): { locale: typeof locale } {
  return { locale };
}

/** 取文案：缺键回退英文（FR-SET-02），再回退键名本身。支持 {name} 占位符。 */
export function t(key: string, params?: Record<string, string | number>): string {
  const dict = dictionaries[locale.value] ?? dictionaries.en;
  return interpolateMessage(dict[key] ?? dictionaries.en[key] ?? key, params);
}

/**
 * 数量文案：扁平后缀键 `<key>.one` / `<key>.other`。
 *
 * - 只接受非负安全整数；负数/小数/NaN/Infinity/不安全整数返回 "—"（不伪装 0）。
 * - 当前 locale 有可用 family（至少 `.other`）时按该语言 Intl.PluralRules 选
 *   category→other；没有 family 时回退英文并重新按英文规则计算。
 * - 最终没有英文 family 时返回 key（开发期暴露；实际调用键由 i18n guard 拒绝）。
 * - `{count}` 由 count 参数唯一决定，不能被 params.count 覆盖。
 */
export function tCount(
  key: string,
  count: number,
  params?: Record<string, string | number>,
): string {
  if (!isValidCount(count)) return INVALID_COUNT_TEXT;
  const template = resolvePluralTemplate(
    dictionaries[locale.value],
    dictionaries.en,
    locale.value,
    key,
    count,
  );
  if (template === undefined) return key;
  return formatCountMessage(template, count, params);
}

/**
 * 是否已注册该文案（当前 locale 或英文兜底）。
 * 仅供错误展示判断“能不能安全显示”，不改变 t() 的缺键回退语义。
 */
export function hasMessageKey(key: string): boolean {
  if (!key) return false;
  const dict = dictionaries[locale.value] ?? {};
  return (
    Object.prototype.hasOwnProperty.call(dict, key) ||
    Object.prototype.hasOwnProperty.call(dictionaries.en, key)
  );
}

/** 字节 -> "1.2 GB"（FR-SET-04 存储单位）。 */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return "—";
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n;
  let i = -1;
  do {
    v /= 1024;
    i += 1;
  } while (v >= 1024 && i < units.length - 1);
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

/** 毫秒 -> "1:02:03"（媒体时长，紧凑）。 */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "—";
  const total = Math.round(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const mm = String(m).padStart(2, "0");
  const ss = String(s).padStart(2, "0");
  return h > 0 ? `${h}:${mm}:${ss}` : `${m}:${ss}`;
}

/** 毫秒 -> "00:01:02,345"（SRT 时间戳风格）。 */
export function formatTimestamp(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "00:00:00,000";
  const h = Math.floor(ms / 3_600_000);
  const m = Math.floor((ms % 3_600_000) / 60_000);
  const s = Math.floor((ms % 60_000) / 1000);
  const msPart = ms % 1000;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")},${String(msPart).padStart(3, "0")}`;
}

/** 百分比（0-100 数值） -> "42%"。 */
export function formatPercent(p: number): string {
  return `${Math.round(p)}%`;
}

/** 本地时间 -> HH:mm:ss（24 小时制，供保存状态等紧凑场景使用）。 */
export function formatClock(value: string | number = Date.now()): string {
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "—";
  const pad = (part: number): string => String(part).padStart(2, "0");
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

/** ISO 时间 -> 全局统一本地时间：YYYY-MM-DD HH:mm:ss。 */
export function formatDate(value: string | number): string {
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "—";
  const pad = (part: number): string => String(part).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}
