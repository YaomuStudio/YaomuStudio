<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref } from "vue";
import { t } from "@/i18n";
import YmIcon from "./YmIcon.vue";

export interface SelectOption {
  value: string;
  label: string;
}

const props = withDefaults(
  defineProps<{
    modelValue: string;
    options: SelectOption[];
    placeholder?: string;
    disabled?: boolean;
    menuMinWidth?: number;
    menuMaxHeight?: number;
    ariaLabel?: string;
  }>(),
  { disabled: false, placeholder: "", menuMaxHeight: 240 },
);
const emit = defineEmits<{ (e: "update:modelValue", v: string): void }>();

const root = ref<HTMLElement | null>(null);
const trigger = ref<HTMLButtonElement | null>(null);
const open = ref(false);
const activeIndex = ref(-1);
const menuStyle = ref<Record<string, string>>({});

const selected = computed(() => props.options.find((option) => option.value === props.modelValue));
const displayLabel = computed(
  () => selected.value?.label ?? (props.placeholder || t("common.selectPlaceholder")),
);

function positionMenu(): void {
  const el = trigger.value;
  if (!el) return;
  const rect = el.getBoundingClientRect();
  const gap = 6;
  const menuWidth = Math.max(rect.width, props.menuMinWidth ?? 0);
  const estimatedHeight = Math.min(props.menuMaxHeight, props.options.length * 34 + 8);
  const below = window.innerHeight - rect.bottom;
  const top =
    below >= estimatedHeight || below >= rect.top
      ? rect.bottom + gap
      : Math.max(8, rect.top - estimatedHeight - gap);
  menuStyle.value = {
    position: "fixed",
    left: `${Math.max(8, Math.min(rect.left, window.innerWidth - menuWidth - 8))}px`,
    top: `${top}px`,
    width: `${menuWidth}px`,
    zIndex: "1200",
  };
}

function openMenu(): void {
  if (props.disabled || open.value) return;
  open.value = true;
  activeIndex.value = Math.max(0, props.options.findIndex((o) => o.value === props.modelValue));
  void nextTick(positionMenu);
}

function closeMenu(): void {
  open.value = false;
  activeIndex.value = -1;
}

function toggle(): void {
  if (open.value) closeMenu();
  else openMenu();
}

function choose(option: SelectOption): void {
  emit("update:modelValue", option.value);
  closeMenu();
}

function onKeydown(event: KeyboardEvent): void {
  if (props.disabled) return;
  if (!open.value) {
    if (["Enter", " ", "ArrowDown", "ArrowUp"].includes(event.key)) {
      event.preventDefault();
      openMenu();
    }
    return;
  }
  if (event.key === "Escape" || event.key === "Tab") {
    closeMenu();
    return;
  }
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    const delta = event.key === "ArrowDown" ? 1 : -1;
    const next = (activeIndex.value + delta + props.options.length) % props.options.length;
    activeIndex.value = next;
    return;
  }
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    const option = props.options[activeIndex.value];
    if (option) choose(option);
  }
}

function onOutside(event: MouseEvent): void {
  if (!root.value) return;
  const target = event.target as Node;
  if (root.value.contains(target)) return;
  const menu = document.getElementById(`ym-select-menu-${id}`);
  if (menu?.contains(target)) return;
  closeMenu();
}

const id = Math.random().toString(36).slice(2);

function onViewportChange(): void {
  // 页面滚动/缩放时重新定位，而不是关闭：选项自身滚动也不能把菜单关掉。
  if (open.value) void nextTick(positionMenu);
}

if (typeof window !== "undefined") {
  document.addEventListener("mousedown", onOutside);
  window.addEventListener("resize", onViewportChange);
  window.addEventListener("scroll", onViewportChange, true);
}
onBeforeUnmount(() => {
  document.removeEventListener("mousedown", onOutside);
  window.removeEventListener("resize", onViewportChange);
  window.removeEventListener("scroll", onViewportChange, true);
});
</script>

<template>
  <div ref="root" class="ym-select" :class="{ 'is-open': open, 'is-disabled': disabled }">
    <button
      ref="trigger"
      type="button"
      class="ym-select__trigger"
      :disabled="disabled"
      role="combobox"
      :aria-expanded="open"
      :aria-controls="`ym-select-menu-${id}`"
      :aria-activedescendant="activeIndex >= 0 ? `ym-select-option-${id}-${activeIndex}` : undefined"
      :aria-label="ariaLabel || undefined"
      aria-haspopup="listbox"
      @click="toggle"
      @keydown="onKeydown"
    >
      <span class="ym-select__value" :class="{ 'is-placeholder': !selected }">{{ displayLabel }}</span>
      <YmIcon class="ym-select__chevron" :name="open ? 'chevron-up' : 'chevron-down'" :size="14" />
    </button>

    <Teleport to="body">
      <div
        v-if="open"
        :id="`ym-select-menu-${id}`"
        class="ym-select__menu"
        :style="{ ...menuStyle, maxHeight: `${menuMaxHeight}px` }"
        role="listbox"
      >
        <button
          v-for="(option, index) in options"
          :id="`ym-select-option-${id}-${index}`"
          :key="option.value"
          :data-value="option.value"
          type="button"
          class="ym-select__option"
          :class="{
            'is-selected': option.value === modelValue,
            'is-active': index === activeIndex,
          }"
          role="option"
          :aria-selected="option.value === modelValue"
          @mouseenter="activeIndex = index"
          @click="choose(option)"
        >
          <span class="ym-select__option-label">{{ option.label }}</span>
          <YmIcon
            v-if="option.value === modelValue"
            name="check"
            :size="14"
            class="ym-select__check"
          />
        </button>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.ym-select {
  position: relative;
  width: 100%;
  min-width: 0;
}
.ym-select__trigger {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
  height: 32px;
  padding: 0 10px 0 12px;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  color: var(--color-text);
  font-size: var(--font-size-md);
  text-align: left;
  transition: border-color var(--motion-duration-base) var(--motion-ease-out), box-shadow var(--motion-duration-base) var(--motion-ease-out);
}
.ym-select__trigger:hover:not(:disabled) {
  border-color: color-mix(in srgb, var(--color-primary) 45%, var(--color-border-strong));
}
.ym-select.is-open .ym-select__trigger,
.ym-select__trigger:focus-visible {
  border-color: var(--color-primary);
  outline: none;
  box-shadow: var(--focus-ring);
}
.ym-select__trigger:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.ym-select__value {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ym-select__value.is-placeholder {
  color: var(--color-text-faint);
}
.ym-select__chevron {
  flex: none;
  color: var(--color-text-faint);
  transition: transform var(--motion-duration-base) var(--motion-ease-out), color var(--motion-duration-base) var(--motion-ease-out);
}
.ym-select.is-open .ym-select__chevron {
  color: var(--color-primary);
}

.ym-select__menu {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 240px;
  overflow-y: auto;
  padding: 4px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: var(--color-surface);
  box-shadow: var(--shadow-lg);
}
.ym-select__option {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
  min-height: 32px;
  padding: 0 8px 0 10px;
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  font-size: var(--font-size-md);
  text-align: left;
}
.ym-select__option.is-active {
  background: var(--color-surface-2);
  color: var(--color-text);
}
.ym-select__option.is-selected {
  background: var(--color-primary-soft);
  color: var(--color-primary);
  font-weight: 500;
}
.ym-select__option-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ym-select__check {
  flex: none;
  color: var(--color-primary);
}
</style>
