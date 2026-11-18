<script setup lang="ts">
import { computed } from "vue";
import { t, tCount } from "@/i18n";
import YmButton from "@/components/ui/YmButton.vue";
import YmDialog from "@/components/ui/YmDialog.vue";

const props = withDefaults(
  defineProps<{
    modelValue: boolean;
    reason: string;
    count?: number;
    refreshing?: boolean;
  }>(),
  { count: 1, refreshing: false },
);

const emit = defineEmits<{
  (e: "update:modelValue", v: boolean): void;
  (e: "queue"): void;
  (e: "refresh"): void;
}>();

const countLabel = computed(() => tCount("onboarding.models.queuedCount", props.count));
</script>

<template>
  <YmDialog
    size="lg"
    :model-value="modelValue"
    :title="t('onboarding.models.environmentGateTitle')"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <p class="queue-body">{{ t("onboarding.models.environmentGateBody") }}</p>
    <p class="queue-reason"><bdi>{{ reason }}</bdi></p>
    <p class="queue-count"><bdi>{{ countLabel }}</bdi></p>
    <template #footer="{ close }">
      <YmButton variant="secondary" @click="close">{{ t("common.cancel") }}</YmButton>
      <YmButton
        variant="secondary"
        :loading="refreshing"
        @click="emit('refresh')"
      >
        {{ t("onboarding.environment.refresh") }}
      </YmButton>
      <YmButton variant="primary" @click="emit('queue')">
        {{ t("onboarding.models.queueAction") }}
      </YmButton>
    </template>
  </YmDialog>
</template>

<style scoped>
.queue-body {
  margin-bottom: var(--space-3);
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.queue-reason,
.queue-count {
  margin: 0 0 var(--space-2);
  font-size: var(--font-size-sm);
}
.queue-count {
  color: var(--color-text-faint);
}
</style>
