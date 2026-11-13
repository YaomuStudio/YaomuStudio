// 通知中心 store：汇总任务、应用更新、系统（Core 连接）通知。
// 只做前端聚合（数据来自 tasks / updater / capabilities），不引入新的后端契约；
// 系统级弹窗仍由原路径按设置下发，这里仅负责站内汇总与已读状态。
import { defineStore } from "pinia";
import { computed, ref } from "vue";

export type NotificationKind = "task" | "update" | "system";
export type NotificationTone = "success" | "error" | "info" | "warning";

export interface AppNotification {
  id: string;
  kind: NotificationKind;
  tone: NotificationTone;
  title: string;
  body: string;
  /** 点击跳转路由（如任务中心）。 */
  route?: string;
  createdAt: number;
  /** 同一类系统通知的去重键；只保留最新一条。 */
  dedupeKey?: string;
}

const READ_KEY = "yaomu.notifications.read";
const MAX_ITEMS = 50;

function loadRead(): Set<string> {
  try {
    const raw = localStorage.getItem(READ_KEY);
    return new Set(raw ? (JSON.parse(raw) as string[]) : []);
  } catch {
    return new Set();
  }
}

export const useNotificationsStore = defineStore("notifications", () => {
  const items = ref<AppNotification[]>([]);
  const readIds = ref<Set<string>>(loadRead());

  const unreadCount = computed(
    () => items.value.filter((item) => !readIds.value.has(item.id)).length,
  );

  /** 同 dedupeKey 的系统类通知只保留最新一条（如 Core 掉线反复触发）。 */
  function push(input: Omit<AppNotification, "createdAt">): void {
    if (input.dedupeKey) {
      items.value = items.value.filter((item) => item.dedupeKey !== input.dedupeKey);
    }
    items.value = [
      { ...input, createdAt: Date.now() },
      ...items.value.filter((item) => item.id !== input.id),
    ].slice(0, MAX_ITEMS);
  }

  function markRead(id: string): void {
    readIds.value = new Set([...readIds.value, id]);
    persist();
  }

  function markAllRead(): void {
    readIds.value = new Set([...readIds.value, ...items.value.map((item) => item.id)]);
    persist();
  }

  function persist(): void {
    try {
      localStorage.setItem(READ_KEY, JSON.stringify([...readIds.value].slice(-MAX_ITEMS * 2)));
    } catch {
      /* 存储不可用时静默降级为会话内已读 */
    }
  }

  return { items, readIds, unreadCount, push, markRead, markAllRead };
});
