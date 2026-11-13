// 响应式安全提示：事件发生时只保存安全键/参数与 displayErrorFields，
// 到渲染 computed 时才调用 t()/errorText()。locale 或语言包变化会重新渲染；
// 已显示提示不会重发操作、不会重置调用方的自动关闭计时器。
//
// 禁止保存已翻译字符串、原始 Error.message/String(error)、未知 message 或 stack。
import type { DisplayErrorFields } from "./errorDisplay";
import { displayErrorFields, errorText } from "./errorDisplay";
import { t, tCount } from "./index";

export type NoticeParamValue = string | number | NoticeMessageParam;

export type NoticeParams = Record<string, NoticeParamValue>;

const NOTICE_KEY_MARK = Symbol("yaomu.noticeKey");

/**
 * 已注册文案参数：事件期只冻结 messageKey，渲染时按当前 locale 解析。
 * 不能传入任意字符串/对象，只有 noticeKey() 能构造（i18n guard 校验字面量键）。
 */
export interface NoticeMessageParam {
  readonly messageKey: string;
  readonly [NOTICE_KEY_MARK]: true;
}

export function noticeKey(key: string): NoticeMessageParam {
  return Object.freeze({ messageKey: key, [NOTICE_KEY_MARK]: true as const });
}

function isNoticeMessageParam(value: unknown): value is NoticeMessageParam {
  return (
    typeof value === "object" &&
    value !== null &&
    (value as NoticeMessageParam)[NOTICE_KEY_MARK] === true &&
    typeof (value as NoticeMessageParam).messageKey === "string"
  );
}

export interface SafeNotice {
  /** 已注册文案键（渲染时按当前语言解析）。 */
  key?: string;
  /** 事件发生时冻结的占位参数；只接受 string/number 或已注册文案参数。 */
  params?: Readonly<NoticeParams>;
  /** 数量文案的冻结数量（使用 `<key>.one/.other` family）。 */
  count?: number;
  /** 安全错误身份（已注册 messageKey / 契约错误码）。 */
  error?: DisplayErrorFields;
  /** 错误无已知 messageKey/code 时使用的已注册回退键。 */
  fallbackKey?: string;
  /** 多段提示：按顺序渲染并用 separator 连接。 */
  parts?: readonly SafeNotice[];
  /** 多段提示分隔符（默认空格）。 */
  separator?: string;
}

/** 复制并冻结参数，避免之后引用被清空或切换的 ref/对象。 */
function snapshotParams(params?: NoticeParams): Readonly<NoticeParams> | undefined {
  if (!params) return undefined;
  const snapshot: NoticeParams = {};
  for (const [key, value] of Object.entries(params)) {
    if (typeof value === "string" || typeof value === "number") snapshot[key] = value;
    else if (isNoticeMessageParam(value)) snapshot[key] = noticeKey(value.messageKey);
  }
  return Object.freeze(snapshot);
}

/** 渲染期把嵌套文案参数翻译为当前语言文本；冻结的参数本身不变。 */
function resolveParams(
  params: Readonly<NoticeParams> | undefined,
): Record<string, string | number> | undefined {
  if (!params) return undefined;
  const resolved: Record<string, string | number> = {};
  for (const [key, value] of Object.entries(params)) {
    resolved[key] = isNoticeMessageParam(value) ? t(value.messageKey) : value;
  }
  return resolved;
}

/** 成功/静态提示：只保存键与冻结参数。 */
export function messageNotice(key: string, params?: NoticeParams): SafeNotice {
  return { key, params: snapshotParams(params) };
}

/** 数量提示：渲染时通过 tCount 按实际 fallback 语言选择复数。 */
export function countNotice(
  key: string,
  count: number,
  params?: NoticeParams,
): SafeNotice {
  return { key, count, params: snapshotParams(params) };
}

/** 多段提示（如导入数量 + 跳过文件夹数量）。 */
export function noticeList(parts: readonly SafeNotice[], separator = " "): SafeNotice {
  return { parts: Object.freeze([...parts]), separator };
}

/**
 * 失败提示：保存 displayErrorFields 与可选前缀键。
 * 前缀键含 `{error}` 时把安全错误文案代入；否则按既有写法追加 “: <error>”。
 */
export function errorNotice(
  value: unknown,
  key?: string,
  params?: NoticeParams,
): SafeNotice {
  return {
    key,
    params: snapshotParams(params),
    error: displayErrorFields(value),
  };
}

/**
 * 错误回退提示：错误自身有已注册 messageKey/契约 code 时显示该文案；
 * 否则显示 fallbackKey。等价于既有 errorText(value, fallbackKey) 的延迟渲染。
 */
export function fallbackErrorNotice(value: unknown, fallbackKey: string): SafeNotice {
  return { error: displayErrorFields(value), fallbackKey };
}

/** 渲染安全提示；没有可展示内容时返回空串。 */
export function renderNotice(notice?: SafeNotice | null): string {
  if (!notice) return "";
  if (notice.parts) {
    return notice.parts
      .map((part) => renderNotice(part))
      .filter(Boolean)
      .join(notice.separator ?? " ");
  }
  const hasError = notice.error !== undefined;
  const errorValue = hasError
    ? errorText(notice.error, notice.fallbackKey)
    : "";
  if (!notice.key) return errorValue;
  const resolved = resolveParams(notice.params);
  const params = hasError ? { ...resolved, error: errorValue } : resolved;
  const text =
    notice.count !== undefined
      ? tCount(notice.key, notice.count, params)
      : t(notice.key, params);
  if (hasError && errorValue && !text.includes(errorValue)) {
    return `${text}: ${errorValue}`;
  }
  return text;
}
