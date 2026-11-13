<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { getLocale, t } from "@/i18n";
import type { Language } from "@/domain/types";
import { targetLanguageLabel } from "@/domain/languages";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmTextInput from "@/components/ui/YmTextInput.vue";

type LanguageOption = Language & { popular?: boolean };

const props = defineProps<{
  modelValue: string[];
  options: LanguageOption[];
  placeholder?: string;
}>();

const emit = defineEmits<{
  (e: "toggle", code: string): void;
  (e: "selectAll"): void;
  (e: "clear"): void;
}>();

const open = ref(false);
const query = ref("");
const root = ref<HTMLElement | null>(null);
const selected = computed(() => new Set(props.modelValue));
const selectedLabels = computed(() =>
  props.modelValue.map((code) => {
    const language = props.options.find((item) => item.code === code);
    return language ? targetLanguageLabel(language, getLocale()) : code;
  }),
);

function isFirstMore(language: LanguageOption): boolean {
  return (
    !language.popular &&
    filtered.value.indexOf(language) === filtered.value.findIndex((item) => !item.popular)
  );
}

const filtered = computed(() => {
  const keyword = query.value.trim().toLowerCase();
  if (!keyword) return props.options;
  return props.options.filter((language) =>
    `${language.code} ${language.name} ${language.nativeName ?? ""}`.toLowerCase().includes(keyword),
  );
});

function closeOnOutside(event: MouseEvent): void {
  if (root.value && !root.value.contains(event.target as Node)) open.value = false;
}

onMounted(() => document.addEventListener("mousedown", closeOnOutside));
onBeforeUnmount(() => document.removeEventListener("mousedown", closeOnOutside));
</script>

<template>
  <div ref="root" class="language-select" :class="{ 'is-open': open }">
    <div
      class="language-select__field"
      data-testid="language-picker-trigger"
      role="combobox"
      tabindex="0"
      aria-haspopup="listbox"
      :aria-expanded="open"
      @click="open = !open"
      @keydown.enter.prevent="open = !open"
      @keydown.space.prevent="open = !open"
      @keydown.esc="open = false"
    >
      <span v-if="!selectedLabels.length" class="language-select__placeholder">
        {{ placeholder ?? t("pipeline.config.languagePlaceholder") }}
      </span>
      <span
        v-for="(label, index) in selectedLabels"
        :key="modelValue[index]"
        class="language-chip"
      >
        {{ label }}
        <button
          type="button"
          :aria-label="t('pipeline.config.removeLanguage')"
          @click.stop="emit('toggle', modelValue[index])"
        >
          <YmIcon name="x" :size="11" />
        </button>
      </span>
      <YmIcon class="language-select__chev" name="chevron-down" :size="16" />
    </div>

    <div v-if="open" class="language-select__panel">
      <div class="language-select__head">
        <span>{{ t("pipeline.config.selected", { count: modelValue.length }) }}</span>
        <div class="language-select__actions">
          <button type="button" @click="emit('selectAll')">
            {{ t("pipeline.config.selectAll") }}
          </button>
          <button type="button" @click="emit('clear')">
            {{ t("pipeline.config.clearAll") }}
          </button>
        </div>
      </div>

      <YmTextInput v-model="query" :placeholder="t('pipeline.config.searchLanguage')" />

      <div v-if="filtered.length" v-scroll-fade class="language-options ym-scroll-fade" role="listbox" aria-multiselectable="true">
        <template v-for="language in filtered" :key="language.code">
          <p v-if="isFirstMore(language)" class="language-options__divider">
            {{ t("pipeline.config.moreLanguages") }}
          </p>
          <button
            type="button"
            class="language-option"
            :class="{ 'is-selected': selected.has(language.code) }"
            role="option"
            :aria-selected="selected.has(language.code)"
            @click="emit('toggle', language.code)"
          >
            <span class="language-option__check">
              <YmIcon v-if="selected.has(language.code)" name="check" :size="11" />
            </span>
            <span class="language-option__label">
              {{ targetLanguageLabel(language, getLocale()) }}
            </span>
          </button>
        </template>
      </div>
      <p v-else class="language-select__empty">{{ t("pipeline.config.languageEmpty") }}</p>
    </div>
  </div>
</template>

<style scoped>
.language-select {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 100%;
}
.language-select__field {
  position: relative;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  width: 100%;
  min-height: 38px;
  padding: 5px 34px 5px 8px;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-control);
  background: var(--color-surface);
  font-size: var(--font-size-md);
  text-align: left;
  cursor: pointer;
  transition:
    border-color var(--motion-duration-snap) var(--motion-ease-out),
    box-shadow var(--motion-duration-snap) var(--motion-ease-out);
}
.language-select__field:hover {
  border-color: var(--color-primary);
}
.language-select__field:focus-visible {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: var(--focus-ring);
}
.language-select.is-open .language-select__field {
  border-color: var(--color-primary);
  box-shadow: var(--focus-ring);
}
.language-select__placeholder {
  color: var(--color-text-faint);
}
.language-select__chev {
  position: absolute;
  top: 50%;
  right: 10px;
  flex: none;
  color: var(--color-text-faint);
  transform: translateY(-50%);
  transition: transform var(--motion-duration-snap) var(--motion-ease-out);
  pointer-events: none;
}
.language-select.is-open .language-select__chev {
  transform: translateY(-50%) rotate(180deg);
}
.language-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  min-height: 24px;
  max-width: 100%;
  padding: 0 3px 0 8px;
  border: 1px solid color-mix(in srgb, var(--color-primary) 20%, transparent);
  border-radius: var(--radius-pill);
  background: color-mix(in srgb, var(--color-primary) 7%, var(--color-surface));
  color: var(--color-primary-ink);
  font-size: var(--font-size-xs);
  font-weight: 500;
  line-height: 1;
}
.language-chip button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 17px;
  height: 17px;
  flex: none;
  border-radius: 50%;
  color: var(--color-primary-ink);
  opacity: 0.55;
  transition:
    background var(--motion-duration-snap) var(--motion-ease-snap),
    opacity var(--motion-duration-snap) var(--motion-ease-snap);
}
.language-chip button:hover {
  background: color-mix(in srgb, var(--color-primary) 12%, transparent);
  opacity: 1;
}
.language-chip button:focus-visible {
  outline: none;
  box-shadow: var(--focus-ring);
}
.language-select__panel {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  box-shadow: var(--shadow-popover);
  transform-origin: top;
  animation: ym-pop-in var(--motion-duration-base) var(--motion-ease-out);
}
@keyframes ym-pop-in {
  from {
    opacity: 0;
    transform: translateY(-4px) scale(0.98);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
.language-select__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
}
.language-select__actions {
  display: flex;
  gap: 10px;
}
.language-select__actions button {
  color: var(--color-primary);
  font-size: var(--font-size-xs);
}
.language-select__actions button:hover {
  text-decoration: underline;
}
.language-options__divider {
  grid-column: 1 / -1;
  margin: 6px 0 2px;
  padding-top: 8px;
  border-top: 1px solid var(--color-border);
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
  font-weight: 600;
}
.language-options {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 4px;
  max-height: 240px;
  overflow-y: auto;
}
.language-option {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  padding: 7px 8px;
  border-radius: 8px;
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  text-align: left;
  transition:
    background var(--motion-duration-snap) var(--motion-ease-snap),
    color var(--motion-duration-snap) var(--motion-ease-snap);
}
.language-option:hover {
  background: var(--color-surface-2);
  color: var(--color-text);
}
.language-option.is-selected {
  background: var(--color-primary-soft);
  color: var(--color-primary);
}
.language-option__check {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  flex: none;
  border: 1px solid var(--color-border-strong);
  border-radius: 4px;
  background: var(--color-surface);
  color: var(--color-on-primary);
}
.language-option.is-selected .language-option__check {
  border-color: var(--color-primary);
  background: var(--color-primary);
}
.language-option__label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.language-select__empty {
  margin: 0;
  padding: 8px 0;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
@media (max-width: 640px) {
  .language-options {
    grid-template-columns: 1fr;
  }
}
</style>
