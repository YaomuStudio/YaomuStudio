<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from "vue";
import YmButton from "./YmButton.vue";

const props = withDefaults(
  defineProps<{
    modelValue: boolean;
    title?: string;
    confirmText?: string;
    cancelText?: string;
    danger?: boolean;
  }>(),
  { danger: false, confirmText: "", cancelText: "" },
);
const emit = defineEmits<{
  (e: "update:modelValue", v: boolean): void;
  (e: "confirm"): void;
  (e: "cancel"): void;
}>();

const dialogEl = ref<HTMLElement | null>(null);
/** 打开前的焦点元素；关闭/卸载时只还给它（仍在文档内时）。 */
let returnFocusTo: HTMLElement | null = null;

const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "[tabindex]:not([tabindex='-1'])",
].join(",");

function restoreTriggerFocus(): void {
  const target = returnFocusTo;
  returnFocusTo = null;
  if (target?.isConnected) target.focus();
}

// 只在当前实例打开时管理焦点；Escape 监听挂在对话框自身，不新增全局监听器。
watch(
  () => props.modelValue,
  (open) => {
    if (open) {
      const active = document.activeElement;
      returnFocusTo = active instanceof HTMLElement ? active : null;
      void nextTick(() => {
        const dialog = dialogEl.value;
        if (!dialog) return;
        const first = dialog.querySelector<HTMLElement>(FOCUSABLE_SELECTOR);
        // 没有可聚焦内容时容器自身可聚焦（tabindex=-1），焦点仍在对话框内。
        (first ?? dialog).focus();
      });
      return;
    }
    restoreTriggerFocus();
  },
  { immediate: true },
);

onBeforeUnmount(restoreTriggerFocus);

function onDialogKeydown(event: KeyboardEvent): void {
  // 输入法组合中的 Escape 用于取消候选，不关闭对话框。
  if (event.key !== "Escape" || event.isComposing) return;
  event.preventDefault();
  close();
}

function close(): void {
  emit("update:modelValue", false);
}
function confirm(): void {
  emit("confirm");
  emit("update:modelValue", false);
}
function cancel(): void {
  emit("cancel");
  emit("update:modelValue", false);
}
</script>

<template>
  <Teleport to="body">
    <Transition name="ym-dialog">
      <div v-if="modelValue" class="ym-dialog__scrim" @click.self="close">
        <div
          ref="dialogEl"
          class="ym-dialog"
          role="dialog"
          aria-modal="true"
          tabindex="-1"
          @keydown="onDialogKeydown"
        >
          <h3 v-if="title" class="ym-dialog__title">{{ title }}</h3>
          <div class="ym-dialog__body"><slot /></div>
          <div class="ym-dialog__footer">
            <slot name="footer" :close="close" :confirm="confirm">
              <YmButton variant="secondary" @click="cancel">{{ cancelText }}</YmButton>
              <YmButton :variant="danger ? 'danger' : 'primary'" @click="confirm">{{ confirmText }}</YmButton>
            </slot>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.ym-dialog__scrim {
  position: fixed;
  inset: 0;
  background: rgba(10, 12, 16, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}
.ym-dialog {
  width: min(420px, calc(100vw - 32px));
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-popover);
  overflow: hidden;
  transform-origin: center;
}
.ym-dialog-enter-active {
  transition: opacity var(--motion-duration-base) var(--motion-ease-out);
}
.ym-dialog-leave-active {
  transition: opacity var(--motion-duration-snap) var(--motion-ease-in);
}
.ym-dialog-enter-from,
.ym-dialog-leave-to {
  opacity: 0;
}
.ym-dialog-enter-active .ym-dialog {
  animation: ym-dialog-in var(--motion-duration-base) var(--motion-ease-out) both;
}
.ym-dialog-leave-active .ym-dialog {
  animation: ym-dialog-out var(--motion-duration-snap) var(--motion-ease-out) both;
}
@keyframes ym-dialog-in {
  from {
    opacity: 0;
    transform: translateY(10px) scale(0.96);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
@keyframes ym-dialog-out {
  from {
    opacity: 1;
    transform: none;
  }
  to {
    opacity: 0;
    transform: translateY(4px) scale(0.98);
  }
}
.ym-dialog__title {
  padding: var(--space-4) var(--space-4) 0;
  font-size: var(--font-size-lg);
}
.ym-dialog__body {
  padding: var(--space-3) var(--space-4);
  color: var(--color-text-muted);
  font-size: var(--font-size-md);
}
.ym-dialog__footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-2);
  padding: var(--space-3) var(--space-4);
  border-top: 1px solid var(--color-border);
}
</style>
