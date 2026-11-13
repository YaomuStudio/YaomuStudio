<script setup lang="ts">
import { computed } from "vue";
import { t } from "@/i18n";
import { runOptionRows } from "@/domain/runOptions";
import type { RunParams } from "@/domain/types";

const props = defineProps<{ params?: RunParams }>();

const rows = computed(() => runOptionRows(props.params));
</script>

<template>
  <section v-if="rows.length" class="run-options" data-testid="run-options">
    <h3 class="run-options__title">{{ t("runOptions.title") }}</h3>
    <dl class="run-options__list">
      <div
        v-for="row in rows"
        :key="row.key"
        class="run-options__row"
        :data-option="row.key"
      >
        <dt>{{ t(row.labelKey) }}</dt>
        <dd :class="{ 'run-options__value--warning': row.warning }">
          {{ t(row.valueKey) }}
        </dd>
      </div>
    </dl>
    <p class="run-options__hint">{{ t("runOptions.frozenHint") }}</p>
  </section>
</template>

<style scoped>
.run-options {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding-top: var(--space-3);
  border-top: 1px solid var(--color-border);
}
.run-options__title {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
  font-weight: 500;
}
.run-options__list {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  margin: 0;
}
.run-options__row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--space-3);
  font-size: var(--font-size-sm);
}
.run-options__row dt {
  flex: none;
  color: var(--color-text-muted);
}
.run-options__row dd {
  min-width: 0;
  margin: 0;
  text-align: right;
  overflow-wrap: anywhere;
}
.run-options__value--warning {
  color: var(--color-warning-strong);
}
.run-options__hint {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
  line-height: var(--line-height-normal);
}
</style>
