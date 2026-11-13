import { onUnmounted, ref, watch } from "vue";
import { api } from "@/api";
import type { PipelineNodeKey, Task } from "@/domain/types";

const POLL_MS = 1000;

/**
 * 按 Run + 节点读取最近一次 Task，供「执行过程」面板使用。
 *
 * 全局 tasks store 只持有启动时加载的首页任务，不保证包含刚创建的任务；
 * 面板直接按 runId 查询，避免因列表分页或事件合并延迟而找不到当前任务。
 * 只读取 Core 状态，不参与业务状态推进。
 */
export function useRunTask(
  runId: () => string | undefined,
  nodeKey: PipelineNodeKey,
  active: () => boolean,
) {
  const task = ref<Task | undefined>();
  let timer: ReturnType<typeof setInterval> | null = null;
  let lastRunId: string | undefined;
  let fetching = false;

  function stopPolling(): void {
    if (timer !== null) {
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
      // Run 已切换时，迟到结果不得写回新 Run。
      if (runId() !== id) return;
      const latest = [...page.items]
        .filter((item) => item.runId === id && item.nodeKey === nodeKey)
        .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))[0];
      if (latest) task.value = latest;
    } catch {
      // 面板是过程反馈；读取失败不改变任务状态，下一轮轮询重试。
    } finally {
      fetching = false;
    }
  }

  watch(
    () => [runId(), active()] as const,
    ([id, isActive]) => {
      const runChanged = id !== lastRunId;
      lastRunId = id;
      stopPolling();
      if (runChanged) task.value = undefined;
      if (!id) return;
      void refresh();
      if (isActive) {
        timer = setInterval(() => {
          void refresh();
        }, POLL_MS);
      }
    },
    { immediate: true },
  );

  onUnmounted(stopPolling);

  return task;
}
