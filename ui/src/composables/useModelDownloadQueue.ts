import { ref } from "vue";
import type { CapabilitySlotKey } from "@/domain/types";

const STORAGE_KEY = "yaomu.model-download-queue.v1";

function readStored(): CapabilitySlotKey[] {
  if (typeof localStorage === "undefined") return [];
  try {
    const raw = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "[]");
    return Array.isArray(raw)
      ? [...new Set(raw.filter((value): value is CapabilitySlotKey => typeof value === "string"))]
      : [];
  } catch {
    return [];
  }
}

const queued = ref<CapabilitySlotKey[]>(readStored());

function persist(): void {
  if (typeof localStorage === "undefined") return;
  localStorage.setItem(STORAGE_KEY, JSON.stringify(queued.value));
}

/** 环境未就绪时的模型下载意图。只排队，不创建 Core 安装记录。 */
export function useModelDownloadQueue() {
  function add(slots: CapabilitySlotKey[]): void {
    const next = new Set(queued.value);
    for (const slot of slots) next.add(slot);
    queued.value = [...next];
    persist();
  }

  function remove(slot: CapabilitySlotKey): void {
    queued.value = queued.value.filter((item) => item !== slot);
    persist();
  }

  function clear(): void {
    queued.value = [];
    persist();
  }

  function has(slot: CapabilitySlotKey): boolean {
    return queued.value.includes(slot);
  }

  return { queued, add, remove, clear, has };
}
