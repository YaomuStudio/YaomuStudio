// 安装 store：模型包下载/校验/安装状态，订阅 Core 的 installation.* 事件。
// 事件由 tasks store 的全局订阅转发。进度事件可丢弃，恢复以 GET /installations 为准；
// 只有本会话内实际发起并跟踪的安装完成时才发系统通知（settings.notifications.downloadDone），
// 避免重连回放历史事件时重复提醒。
import { defineStore } from "pinia";
import { ref } from "vue";
import { t } from "@/i18n";
import { api } from "@/api";
import { CAPABILITY_SLOTS } from "@/domain/capabilities";
import { installationCoveredSlots } from "@/domain/installations";
import type { DomainEvent, ID, Installation, InstallationPhase } from "@/domain/types";
import { useCapabilitiesStore } from "./capabilities";
import { useSettingsStore } from "./settings";

const INSTALLATION_PHASES: readonly InstallationPhase[] = [
  "planning",
  "downloading",
  "verifying",
  "installing",
  "done",
  "failed",
  "canceled",
];

function isInstallationPhase(value: unknown): value is InstallationPhase {
  return typeof value === "string" && (INSTALLATION_PHASES as readonly string[]).includes(value);
}

function eventData(event: DomainEvent): Record<string, unknown> {
  return typeof event.data === "object" && event.data !== null
    ? (event.data as Record<string, unknown>)
    : {};
}

export const useInstallationsStore = defineStore("installations", () => {
  const items = ref<Installation[]>([]);
  const loading = ref(false);
  let reloadTimer: ReturnType<typeof setTimeout> | null = null;
  let capabilityTimer: ReturnType<typeof setTimeout> | null = null;
  /** 本会话内发起且尚未到达终态的安装 ID → 发起槽（只用于通知去重/回退文案）。 */
  const tracked = new Map<ID, string>();

  async function load(): Promise<void> {
    loading.value = true;
    try {
      items.value = await api.getInstallations();
    } finally {
      loading.value = false;
    }
  }

  /** 登记本会话发起的安装；同一物理 ID 只登记一次，完成时只通知一次。 */
  function track(id: ID, slot = ""): void {
    if (id) tracked.set(id, slot);
  }

  function scheduleReload(): void {
    if (reloadTimer) return;
    reloadTimer = setTimeout(() => {
      reloadTimer = null;
      void load().catch(() => undefined);
    }, 200);
  }

  /**
   * 安装到达终态后刷新能力目录：槽位的 READY/FAILED 由 Core 按安装记录推导，
   * 失败/取消的升级不激活新版本，槽位状态可能与下载前不同。
   */
  function refreshCapabilities(): void {
    if (capabilityTimer) return;
    capabilityTimer = setTimeout(() => {
      capabilityTimer = null;
      void useCapabilitiesStore()
        .load()
        .catch(() => undefined);
    }, 300);
  }

  function slotLabel(slot: string): string {
    const meta = CAPABILITY_SLOTS.find((entry) => entry.key === slot);
    return meta ? t(meta.nameKey) : slot;
  }

  function notifyCompleted(slot: string): void {
    if (!useSettingsStore().notifications.downloadDone) return;
    const notify = typeof window !== "undefined" ? window.yaomu?.api?.notify : undefined;
    if (!notify) return;
    void notify(
      t("notify.downloadDone.title"),
      t("notify.downloadDone.body", { name: slotLabel(slot) }),
      "download",
    ).catch(() => undefined);
  }

  /** 同一物理安装只通知一次；共享覆盖时列出全部槽位，不按槽乘出多条通知。 */
  function completedLabel(id: ID, fallbackSlot: string): string {
    const existing = items.value.find((item) => item.id === id);
    if (!existing) return slotLabel(fallbackSlot);
    return installationCoveredSlots(existing)
      .map((slot) => slotLabel(slot))
      .join(", ");
  }

  function applyProgress(id: ID, phase: unknown, progress: unknown): void {
    items.value = items.value.map((item) => {
      if (item.id !== id) return item;
      return {
        ...item,
        phase: isInstallationPhase(phase) ? phase : item.phase,
        progress:
          typeof progress === "number" && Number.isFinite(progress) ? progress : item.progress,
      };
    });
  }

  /** 由全局事件订阅转发；只处理 installation.*。 */
  function handleEvent(event: DomainEvent): void {
    if (!event.type.startsWith("installation.")) return;
    const data = eventData(event);
    const id = typeof data.installation_id === "string" ? data.installation_id : "";

    if (event.type === "installation.progress") {
      if (id) applyProgress(id, data.phase, data.progress);
      return;
    }
    if (event.type === "installation.completed") {
      const trackedSlot = tracked.get(id);
      const wasTracked = tracked.delete(id);
      applyProgress(id, "done", 100);
      scheduleReload();
      refreshCapabilities();
      if (!wasTracked) return;
      const slot = trackedSlot ?? (typeof data.slot === "string" ? data.slot : "");
      notifyCompleted(completedLabel(id, slot));
      return;
    }
    if (event.type === "installation.failed") {
      tracked.delete(id);
      scheduleReload();
      refreshCapabilities();
      return;
    }
    if (event.type === "installation.phase_changed") {
      if (data.phase === "canceled") {
        tracked.delete(id);
        scheduleReload();
        refreshCapabilities();
        return;
      }
    }
    scheduleReload();
  }

  return {
    items,
    loading,
    load,
    track,
    handleEvent,
  };
});
