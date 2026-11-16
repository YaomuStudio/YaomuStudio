<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { api } from "@/api";
import { t } from "@/i18n";
import type { ExecutionLogLine, TaskStatus } from "@/domain/types";
import { runnerWaitingMessageKey } from "@/domain/status";
import { useExecutionLogsStore } from "@/stores/executionLogs";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmCard from "@/components/ui/YmCard.vue";

const props = withDefaults(
  defineProps<{
    /** 当前 Run + 节点对应的最近一次 Task；为空时只在 active 时显示空态。 */
    taskId?: string;
    active?: boolean;
    /** 当前节点任务状态：等待执行器时不能谎称“实时”。 */
    taskStatus?: TaskStatus;
    /** 等待执行器时的任务 messageKey，用来区分未配置、启动中和已隔离。 */
    waitingMessageKey?: string;
  }>(),
  { taskId: "", active: false, taskStatus: undefined, waitingMessageKey: undefined },
);

const logs = useExecutionLogsStore();
const loading = ref(false);
const loadError = ref("");
const scroller = ref<HTMLElement | null>(null);

const lines = computed<ExecutionLogLine[]>(() =>
  props.taskId ? logs.linesFor(props.taskId) : [],
);
/** 任务在等执行器时不会产出任何日志，面板必须说明等待原因而不是显示“实时空态”。 */
const waitingForRunner = computed(() => props.taskStatus === "WAITING_RESOURCE");

async function scrollToBottom(): Promise<void> {
  await nextTick();
  const element = scroller.value;
  if (element) element.scrollTop = element.scrollHeight;
}

async function load(taskId: string): Promise<void> {
  loading.value = true;
  loadError.value = "";
  try {
    const result = await api.getExecutionLog(taskId, { tail: 200 });
    logs.seed(taskId, result.attempt, result.lines);
  } catch {
    loadError.value = t("tools.executionLog.loadFailed");
  } finally {
    loading.value = false;
    await scrollToBottom();
  }
}

watch(
  () => props.taskId,
  (taskId) => {
    if (taskId) void load(taskId);
  },
  { immediate: true },
);

watch(
  () => lines.value.length,
  () => {
    void scrollToBottom();
  },
);
</script>

<template>
  <YmCard v-if="taskId || active">
    <template #header>
      <div class="execution-log__head">
        <span>{{ t("tools.executionLog.title") }}</span>
        <YmBadge v-if="waitingForRunner" tone="neutral">
          {{ t("state.waitingResource") }}
        </YmBadge>
        <YmBadge v-else-if="active" tone="info">{{ t("tools.executionLog.live") }}</YmBadge>
        <YmBadge v-else tone="neutral">{{ t("tools.executionLog.done") }}</YmBadge>
      </div>
    </template>
    <div
      ref="scroller"
      v-scroll-fade
      class="execution-log__body ym-scroll-fade"
      role="log"
      aria-live="polite"
      tabindex="0"
    >
      <p v-if="loadError" class="execution-log__state">{{ loadError }}</p>
      <p v-else-if="lines.length === 0" class="execution-log__state">
        {{
          loading
            ? t("tools.executionLog.loading")
            : waitingForRunner
              ? t(runnerWaitingMessageKey(props.waitingMessageKey))
              : t("tools.executionLog.empty")
        }}
      </p>
      <pre v-else class="execution-log__lines mono"><code
        v-for="line in lines"
        :key="`${line.seq}:${line.at ?? ''}`"
      >{{ line.line }}</code></pre>
    </div>
  </YmCard>
</template>

<style scoped>
.execution-log__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}
.execution-log__body {
  max-height: 220px;
  overflow: auto;
  padding: var(--space-2) var(--space-3);
  background: var(--color-surface-2);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.execution-log__body:focus-visible {
  outline: none;
  box-shadow: var(--focus-ring);
}
.execution-log__state {
  margin: 0;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
  line-height: var(--line-height-normal);
}
.execution-log__lines {
  margin: 0;
  font-size: var(--font-size-xs);
  line-height: 1.5;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.execution-log__lines code {
  display: block;
}
</style>
