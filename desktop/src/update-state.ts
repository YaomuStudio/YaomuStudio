/**
 * 更新状态重放的纯选择逻辑。
 *
 * Main 在 Renderer 首次订阅/刷新后需要补发有限状态；这里只决定发哪个事件，
 * 不依赖 Electron、不发网络请求、不持有业务状态。
 */

export type UpdateReplayChannel =
  | "update:available"
  | "update:downloaded"
  | "update:not-available";

export interface UpdateReplayEvent {
  channel: UpdateReplayChannel;
  payload: { version?: string };
}

/**
 * 选择要重放的事件。
 * - 已发现的新版本优先于旧下载版本；
 * - 同一版本已下载时重放 downloaded，避免降级为 available；
 * - 无版本时明确回 not-available，让 UI 从 checking 回到 idle。
 */
export function selectUpdateReplay(
  downloadedVersion: string | null,
  availableVersion: string | null,
): UpdateReplayEvent {
  if (availableVersion !== null && availableVersion !== downloadedVersion) {
    return { channel: "update:available", payload: { version: availableVersion } };
  }
  if (downloadedVersion !== null) {
    return { channel: "update:downloaded", payload: { version: downloadedVersion } };
  }
  if (availableVersion !== null) {
    return { channel: "update:available", payload: { version: availableVersion } };
  }
  return { channel: "update:not-available", payload: {} };
}
