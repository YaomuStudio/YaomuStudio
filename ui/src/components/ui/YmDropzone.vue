<script setup lang="ts">
import { ref } from "vue";
import YmIcon from "./YmIcon.vue";

const props = withDefaults(
  defineProps<{
    title: string;
    hint?: string;
    accept?: string;
    multiple?: boolean;
    compact?: boolean;
    disabled?: boolean;
    icon?: string;
    manual?: boolean;
  }>(),
  { accept: "", multiple: false, compact: false, disabled: false, manual: false, icon: "upload" },
);
const emit = defineEmits<{
  (e: "files", files: File[]): void;
  (e: "pick"): void;
}>();

const input = ref<HTMLInputElement | null>(null);
const dragging = ref(false);

function onClick(event: MouseEvent): void {
  if (!props.manual) return;
  event.preventDefault();
  emit("pick");
}

function onChange(): void {
  const files = Array.from(input.value?.files ?? []);
  if (files.length) emit("files", files);
  if (input.value) input.value.value = "";
}

function onDrop(event: DragEvent): void {
  if (input.value?.disabled) return;
  dragging.value = false;
  const files = Array.from(event.dataTransfer?.files ?? []);
  if (files.length) emit("files", files);
}
</script>

<template>
  <label
    class="ym-dropzone"
    :class="{ 'is-compact': compact, 'is-disabled': disabled, 'is-dragging': dragging }"
    @click="onClick"
    @dragover.prevent="dragging = true"
    @dragleave.prevent="dragging = false"
    @drop.prevent="onDrop"
  >
    <input
      ref="input"
      class="ym-dropzone__input"
      type="file"
      :accept="accept || undefined"
      :multiple="multiple"
      :disabled="disabled || manual"
      @change="onChange"
    />
    <span class="ym-dropzone__icon">
      <YmIcon :name="icon" :size="compact ? 18 : 24" />
    </span>
    <span class="ym-dropzone__text">
      <span class="ym-dropzone__title">{{ title }}</span>
      <span v-if="hint" class="ym-dropzone__hint">{{ hint }}</span>
    </span>
    <slot />
  </label>
</template>

<style scoped>
.ym-dropzone {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  width: 100%;
  padding: 22px 18px;
  border: 1px dashed color-mix(in srgb, var(--color-primary) 24%, var(--color-border));
  border-radius: var(--radius-md);
  background: color-mix(in srgb, var(--color-primary) 3%, var(--color-surface));
  color: var(--color-text-muted);
  text-align: center;
  cursor: pointer;
  transition: border-color var(--motion-duration-base) var(--motion-ease-out), background var(--motion-duration-base) var(--motion-ease-out), box-shadow var(--motion-duration-base) var(--motion-ease-out);
}
.ym-dropzone:hover:not(.is-disabled),
.ym-dropzone.is-dragging {
  border-color: var(--color-primary);
  background: color-mix(in srgb, var(--color-primary) 6%, var(--color-surface));
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-primary) 8%, transparent);
}
.ym-dropzone:focus-within {
  border-color: var(--color-primary);
  box-shadow: var(--focus-ring);
}
.ym-dropzone.is-compact {
  flex-direction: row;
  justify-content: flex-start;
  padding: 10px 12px;
  text-align: left;
}
.ym-dropzone.is-disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.ym-dropzone__input {
  position: absolute;
  width: 1px;
  height: 1px;
  opacity: 0;
  pointer-events: none;
}
.ym-dropzone__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 40px;
  height: 40px;
  flex: none;
  border-radius: var(--radius-control);
  background: var(--color-primary-soft);
  color: var(--color-primary);
}
.ym-dropzone.is-compact {
  border-color: var(--color-border);
  background: var(--color-surface);
}
.ym-dropzone.is-compact:hover:not(.is-disabled) {
  border-color: color-mix(in srgb, var(--color-primary) 38%, var(--color-border));
  background: color-mix(in srgb, var(--color-primary) 4%, var(--color-surface));
}
.ym-dropzone.is-compact .ym-dropzone__icon {
  width: 28px;
  height: 28px;
  border-radius: var(--radius-sm);
}
.ym-dropzone__text {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.ym-dropzone__title {
  color: var(--color-text);
  font-size: var(--font-size-md);
  font-weight: 600;
  line-height: 1.35;
}
.ym-dropzone.is-compact .ym-dropzone__title {
  font-size: var(--font-size-sm);
  font-weight: 500;
}
.ym-dropzone__hint {
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
  line-height: 1.45;
}
</style>
