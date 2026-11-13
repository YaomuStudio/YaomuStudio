<script setup lang="ts">
import { computed } from "vue";
import { t, formatPercent } from "@/i18n";
import type { NodeStatus, PipelineNodeKey, PipelineNodeState, WorkflowRun } from "@/domain/types";
import {
  nodeStatusLabelKey,
  nodeStatusTone,
  runStatusLabelKey,
  runStatusTone,
  type Tone,
} from "@/domain/status";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmProgress from "@/components/ui/YmProgress.vue";

const props = defineProps<{
  nodes: PipelineNodeState[];
  run?: WorkflowRun | null;
  currentKey?: PipelineNodeKey | null;
}>();

const emit = defineEmits<{ (e: "select", key: PipelineNodeKey): void }>();

const NODE_LABEL_KEYS: Record<PipelineNodeKey, string> = {
  "01_import": "pipeline.node.01_import",
  "02_analyze": "pipeline.node.02_analyze",
  "03_separate": "pipeline.node.03_separate",
  "04_extract": "pipeline.node.04_extract",
  "05_erase": "pipeline.node.05_erase",
  "06_translate": "pipeline.node.06_translate",
  "07_clone": "pipeline.node.07_clone",
  "08_mix": "pipeline.node.08_mix",
};

const DONE_STATUSES = new Set<NodeStatus>(["done", "reused", "skipped"]);

// 节点 06 翻译、07 配音、08 交付在右侧有可回看的产出。
const QUICK_LINKS: { key: PipelineNodeKey; labelKey: string; icon: string }[] = [
  { key: "06_translate", labelKey: "pipeline.node.06_translate", icon: "globe" },
  { key: "07_clone", labelKey: "pipeline.node.07_clone", icon: "mic" },
  { key: "08_mix", labelKey: "pipeline.node.08_mix", icon: "download" },
];

const runLabel = computed(() => (props.run ? t(runStatusLabelKey(props.run.status)) : ""));
const runTone = computed<Tone>(() => (props.run ? runStatusTone(props.run.status) : "neutral"));

/** 总进度 = 各节点进度均值；节点未开始时 progress 为 0，不额外伪造。 */
const overallPct = computed(() =>
  props.nodes.length
    ? Math.round(props.nodes.reduce((sum, node) => sum + node.progress, 0) / props.nodes.length)
    : 0,
);

const currentNode = computed(() =>
  props.currentKey ? props.nodes.find((node) => node.key === props.currentKey) : undefined,
);

/** 当前阶段文案：优先“阶段 · 状态”，无运行节点时退回 Run 状态。 */
const phaseText = computed(() => {
  if (!props.run) return t("pipeline.noRun");
  if (currentNode.value) {
    return `${t(NODE_LABEL_KEYS[currentNode.value.key])} · ${t(
      nodeStatusLabelKey(currentNode.value.status),
    )}`;
  }
  return runLabel.value;
});

function quickDone(key: PipelineNodeKey): boolean {
  const node = props.nodes.find((item) => item.key === key);
  return !!node && DONE_STATUSES.has(node.status);
}

function select(key: PipelineNodeKey): void {
  emit("select", key);
}
</script>

<template>
  <section class="pipeline-phase" data-testid="pipeline-phase">
    <div class="pipeline-phase__main">
      <div class="pipeline-phase__titles">
        <h2 class="pipeline-phase__title">{{ t("pipeline.pipelineTitle") }}</h2>
        <p class="pipeline-phase__hint">{{ t("pipeline.pipelineHint") }}</p>
      </div>
      <div class="pipeline-phase__status">
        <YmBadge :tone="runTone">{{ runLabel || t("pipeline.noRun") }}</YmBadge>
        <span v-if="phaseText" class="pipeline-phase__current">{{ phaseText }}</span>
      </div>
    </div>

    <div class="pipeline-phase__bar">
      <YmProgress
        :value="overallPct"
        :tone="runTone"
        :aria-label="t('pipeline.pipelineTitle')"
      />
      <span class="pipeline-phase__pct mono">
        <bdi dir="ltr">{{ formatPercent(overallPct) }}</bdi>
      </span>
    </div>

    <nav v-if="run" class="pipeline-phase__links" :aria-label="t('pipeline.pipelineTitle')">
      <button
        v-for="link in QUICK_LINKS"
        :key="link.key"
        type="button"
        class="pipeline-phase__link"
        :class="{ 'is-done': quickDone(link.key) }"
        @click="select(link.key)"
      >
        <YmIcon :name="link.icon" :size="14" />
        <span>{{ t(link.labelKey) }}</span>
        <YmIcon v-if="quickDone(link.key)" name="check" :size="12" class="pipeline-phase__check" />
      </button>
    </nav>
  </section>
</template>

<style scoped>
.pipeline-phase {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-4);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
}
.pipeline-phase__main {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-3);
}
.pipeline-phase__titles {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.pipeline-phase__title {
  margin: 0;
  font-size: var(--font-size-md);
  font-weight: 600;
  color: var(--color-text);
}
.pipeline-phase__hint {
  margin: 0;
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.pipeline-phase__status {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-shrink: 0;
  flex-wrap: wrap;
  justify-content: flex-end;
}
.pipeline-phase__current {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.pipeline-phase__bar {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.pipeline-phase__bar .ym-progress {
  flex: 1;
}
.pipeline-phase__pct {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
  min-width: 42px;
  text-align: right;
  flex: none;
}
.pipeline-phase__links {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.pipeline-phase__link {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  background: var(--color-surface-2);
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  cursor: pointer;
  transition:
    color var(--motion-duration-snap) var(--motion-ease-snap),
    border-color var(--motion-duration-snap) var(--motion-ease-snap);
}
.pipeline-phase__link:hover {
  color: var(--color-text);
  border-color: var(--color-border-strong);
}
.pipeline-phase__link:focus-visible {
  box-shadow: var(--focus-ring);
}
.pipeline-phase__link.is-done {
  color: var(--color-primary-ink);
  border-color: var(--color-primary-soft);
}
.pipeline-phase__check {
  color: var(--color-success);
}
</style>
