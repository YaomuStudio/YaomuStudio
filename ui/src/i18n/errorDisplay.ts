// 用户可见错误/预检问题的统一安全展示。
//
// 解析顺序：已注册 messageKey（当前 locale → 英文）→ 契约内已知 ErrorCode 文案 →
// 固定通用回退。绝不返回 String(error)、Error.message、未知 code 或原始键，
// 避免把后端原始输出、密钥或内部键直接暴露给用户。
import { ApiError } from "@/api/types";
import type { PreflightIssue } from "@/domain/types";
import errorCodeMessages from "./errorCodes.json";
import { hasMessageKey, t } from "./index";

/** 契约 ErrorCode → 已注册文案键（唯一来源，脚本会校验两侧都已注册）。 */
const KNOWN_ERROR_MESSAGES: Readonly<Record<string, string>> = errorCodeMessages;

export interface DisplayErrorFields {
  messageKey?: string;
  code?: string;
}

export function displayErrorFields(value: unknown): DisplayErrorFields {
  if (typeof value === "string") return { messageKey: value };
  if (value instanceof ApiError) return { messageKey: value.messageKey, code: value.code };
  if (value && typeof value === "object") {
    const record = value as { messageKey?: unknown; code?: unknown };
    return {
      messageKey: typeof record.messageKey === "string" ? record.messageKey : undefined,
      code: typeof record.code === "string" ? record.code : undefined,
    };
  }
  return {};
}

/** 只承认契约内已知错误码；未知 code 不当作可展示的诊断标识。 */
export function knownErrorCode(value: unknown): string {
  if (typeof value !== "string") return "";
  const normalized = value.trim().toUpperCase();
  return Object.prototype.hasOwnProperty.call(KNOWN_ERROR_MESSAGES, normalized) ? normalized : "";
}

/**
 * 把任意错误或已解码的 messageKey/code 解析为可读文案。
 * 在渲染/computed 中调用，随 locale 与语言包加载响应式更新。
 */
export function errorText(value: unknown, fallbackKey = "error.unknown"): string {
  const { messageKey, code } = displayErrorFields(value);
  if (messageKey && hasMessageKey(messageKey)) return t(messageKey);
  const known = knownErrorCode(code);
  if (known) return t(KNOWN_ERROR_MESSAGES[known]);
  return hasMessageKey(fallbackKey) ? t(fallbackKey) : t("error.unknown");
}

/** 仅对契约内已知错误码返回可展示诊断码；未知 code 不当作安全标识。 */
export function diagnosticErrorCode(value: unknown): string {
  return knownErrorCode(displayErrorFields(value).code);
}

function normalizedActionIds(actionIds: readonly string[] | undefined): string[] {
  if (!Array.isArray(actionIds)) return [];
  return [
    ...new Set(
      actionIds
        .filter((id): id is string => typeof id === "string")
        .map((id) => id.trim())
        .filter(Boolean),
    ),
  ].sort();
}

/**
 * 预检问题的去重键：severity/code/messageKey/规范化 actions 完全相同才算同义。
 * warning 与 blocker、不同恢复动作、不同原因都保留。
 * 用 JSON tuple 序列化，避免字段或 action 内含分隔符时与拼串碰撞。
 */
export function preflightIssueKey(issue: PreflightIssue): string {
  return JSON.stringify([
    issue.severity,
    issue.code.trim(),
    issue.messageKey.trim(),
    normalizedActionIds(issue.actionIds),
  ]);
}

export function dedupePreflightIssues(issues: readonly PreflightIssue[]): PreflightIssue[] {
  const seen = new Set<string>();
  const unique: PreflightIssue[] = [];
  for (const issue of issues) {
    const key = preflightIssueKey(issue);
    if (seen.has(key)) continue;
    seen.add(key);
    unique.push(issue);
  }
  return unique;
}
