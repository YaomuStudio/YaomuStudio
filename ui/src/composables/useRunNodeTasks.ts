// 创建页的节点级任务状态：轮询 Run 下的任务，用于把 WAITING_RESOURCE 如实显示为等待资源。
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { api } from "@/api";
import type { Task } from "@/domain/types";

export function useRunNodeTasks(runId: () => string | undefined, active: () => boolean) {
  const tasks = ref<Task[]>([]);
  let timer: ReturnType<typeof setInterval> | null = null;
  let fetching = false;
  let lastRunId = "";

  function stop(): void {
    if (timer) {
      clearInterval(timer);
      timer = null;
    }
  }

  async function refresh(): Promise<void> {
    const id = runId();
    if (!id || fetching) return;
    fetching = true;
    try {
      const page = await api.listTasks({ runId: id, limit: 200 });
      if (runId() !== id) return;
      tasks.value = page.items;
    } catch {
      // 过程读取失败不改变 Run 事实；下一轮轮询重试。
    } finally {
      fetching = false;
    }
  }

  watch(
    () => [runId(), active()] as const,
    ([id, isActive]) => {
      stop();
      const current = id ?? "";
      if (current !== lastRunId) {
        lastRunId = current;
        tasks.value = [];
      }
      if (!id) return;
      void refresh();
      if (isActive) timer = setInterval(refresh, 1000);
    },
    { immediate: true },
  );

  onBeforeUnmount(stop);

  const byNode = computed(() => {
    const map = new Map<string, Task>();
    for (const task of tasks.value) {
      if (task.kind !== "node" || !task.nodeKey) continue;
      const prev = map.get(task.nodeKey);
      if (!prev || task.updatedAt > prev.updatedAt) map.set(task.nodeKey, task);
    }
    return map;
  });

  const waiting = computed(() => {
    const hasWaiting = tasks.value.some((task) => task.status === "WAITING_RESOURCE");
    const hasRunning = tasks.value.some((task) => task.status === "RUNNING");
    return hasWaiting && !hasRunning;
  });

  return { tasks, byNode, waiting };
}
