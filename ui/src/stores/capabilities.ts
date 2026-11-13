// 能力 store：槽位状态、设备、存储、系统（顶栏/状态栏/模型管理共用）。
import { defineStore } from "pinia";
import { ref } from "vue";
import { api, ApiError } from "@/api";
import { createReadVersion, currentReadVersion } from "@/composables/readGeneration";
import type { Capability, DeviceInfo, StorageUsage } from "@/domain/types";

export const useCapabilitiesStore = defineStore("capabilities", () => {
  const slots = ref<Capability[]>([]);
  const devices = ref<DeviceInfo[]>([]);
  const storage = ref<StorageUsage | null>(null);
  const coreReady = ref(false);
  /** 最近一次加载失败的可读文案键（例如 CORE_NOT_READY）。 */
  const errorKey = ref("");

  /** 只读版本守卫：旧 generation/更早请求的回包不写回当前缓存。 */
  const reads = createReadVersion();
  // 同一连接内的并发 load 单飞：路由守卫和首次 ready 刷新共享同一结果，
  // 避免后发请求把守卫等待的读取作废后又读到空目录。
  let inFlight: { generation: number; epoch: number; promise: Promise<void> } | null = null;

  function load(): Promise<void> {
    const version = currentReadVersion();
    if (
      inFlight &&
      inFlight.generation === version.generation &&
      inFlight.epoch === version.epoch
    ) {
      return inFlight.promise;
    }
    const promise = runLoad().finally(() => {
      if (inFlight?.promise === promise) inFlight = null;
    });
    inFlight = { generation: version.generation, epoch: version.epoch, promise };
    return promise;
  }

  async function runLoad(): Promise<void> {
    const token = reads.begin();
    try {
      const [caps, devs, store, sys] = await Promise.all([
        api.getCapabilities(),
        api.getDevices(),
        api.getStorageUsage(),
        api.getSystem(),
      ]);
      if (!reads.current(token)) return;
      slots.value = caps;
      devices.value = devs;
      storage.value = store;
      coreReady.value = sys.coreReady;
      errorKey.value = "";
    } catch (err) {
      // 旧 generation 的失败不覆盖新连接已写入的状态，也不把错误抛给已恢复的页面。
      if (!reads.current(token)) return;
      // 能力目录读取失败即视为 Core 未就绪，不能停留在上一次的 ready 事实。
      coreReady.value = false;
      errorKey.value = err instanceof ApiError ? err.messageKey : "api.coreNotReady";
      throw err;
    }
  }

  function slotState(key: string): Capability | undefined {
    return slots.value.find((s) => s.slot === key);
  }

  /**
   * 宿主 runtime 已离开 ready（restarting/failed 等）：立即把可用性置 false，
   * 与失联 banner 一致；不清理槽位/项目/草稿等业务缓存。
   * 作废在飞读取，避免旧连接晚回的成功结果把状态重新翻绿。
   */
  function markRuntimeUnavailable(): void {
    reads.invalidate();
    inFlight = null;
    coreReady.value = false;
  }

  return { slots, devices, storage, coreReady, errorKey, load, slotState, markRuntimeUnavailable };
});
