<script setup lang="ts">
import { computed } from "vue";
import type { Tone } from "@/domain/status";

const props = withDefaults(
  defineProps<{
    /** 0-100 数值；缺失/非有限值且未显式指定 indeterminate 时显示不确定态。 */
    value?: number;
    tone?: Tone;
    /** 显式不确定态（未知百分比/运行中尚无有效计数），不伪造 0%。 */
    indeterminate?: boolean;
  }>(),
  { tone: "accent", indeterminate: false },
);

const known = computed(
  () =>
    !props.indeterminate &&
    typeof props.value === "number" &&
    Number.isFinite(props.value),
);
const pct = computed(() =>
  Math.max(0, Math.min(100, Math.round(props.value ?? 0))),
);
</script>

<template>
  <div
    class="ym-progress"
    :class="{ 'ym-progress--indeterminate': !known }"
    role="progressbar"
    :aria-valuenow="known ? pct : undefined"
    aria-valuemin="0"
    aria-valuemax="100"
    :aria-busy="known ? undefined : 'true'"
    :data-indeterminate="known ? undefined : 'true'"
  >
    <div
      class="ym-progress__bar"
      :class="`ym-progress__bar--${tone}`"
      :style="known ? { width: `${pct}%` } : undefined"
    ></div>
  </div>
</template>

<style scoped>
.ym-progress {
  width: 100%;
  height: 6px;
  background: var(--color-surface-3);
  border-radius: var(--radius-pill);
  overflow: hidden;
}
.ym-progress__bar {
  height: 100%;
  border-radius: var(--radius-pill);
  transition: width var(--motion-duration-sheet) var(--motion-ease-out);
}
.ym-progress--indeterminate .ym-progress__bar {
  width: 38%;
  min-width: 28px;
  transition: none;
  animation: ym-progress-indeterminate var(--motion-duration-indeterminate) var(--motion-ease-in-out) infinite;
}
.ym-progress__bar--accent {
  background: var(--gradient-primary);
}
.ym-progress__bar--info {
  background: var(--gradient-primary);
}
.ym-progress__bar--success {
  background: var(--color-success);
}
.ym-progress__bar--warning {
  background: var(--color-warning);
}
.ym-progress__bar--error {
  background: var(--color-error);
}
.ym-progress__bar--neutral {
  background: var(--color-text-faint);
}
@keyframes ym-progress-indeterminate {
  0% {
    transform: translateX(-110%);
  }
  100% {
    transform: translateX(320%);
  }
}
@media (prefers-reduced-motion: reduce) {
  .ym-progress--indeterminate .ym-progress__bar {
    animation: none;
    transform: none;
  }
}
</style>
