<script setup lang="ts">
// 首次宿主就绪前的初始化面板：只提供本地语言/主题控件与安全状态/重试，
// 不挂载业务页、不读取 Core、不展示原始错误或传输细节。
import { computed } from "vue";
import { t } from "@/i18n";
import { UI_LANGUAGES } from "@/domain/languages";
import { runtimePhaseLabel, runtimeReasonLabel } from "./labels";
import { useRuntimeStore } from "@/stores/runtime";
import { useSettingsStore } from "@/stores/settings";
import YmButton from "@/components/ui/YmButton.vue";
import YmField from "@/components/ui/YmField.vue";
import YmSegmented from "@/components/ui/YmSegmented.vue";
import YmSelect from "@/components/ui/YmSelect.vue";
import YmSpinner from "@/components/ui/YmSpinner.vue";

const runtime = useRuntimeStore();
const settings = useSettingsStore();

const languageOptions = computed(() =>
  UI_LANGUAGES.map((language) => ({
    value: language.code,
    label: language.nativeName ?? language.name,
  })),
);

const themeOptions = computed(() => [
  { value: "light", label: t("runtime.init.theme.light") },
  { value: "dark", label: t("runtime.init.theme.dark") },
  { value: "system", label: t("runtime.init.theme.system") },
]);

const phaseLabel = computed(() => runtimePhaseLabel(runtime.phase));
const reasonLabel = computed(() =>
  runtime.reason ? runtimeReasonLabel(runtime.reason) : "",
);
const accessErrorText = computed(() =>
  runtime.accessErrorKey ? t(runtime.accessErrorKey) : "",
);
const canRetry = computed(() => runtime.phase === "failed" && runtime.canRetry);

function onLocale(value: string): void {
  settings.setLocaleAndApply(value);
}

function onTheme(value: string): void {
  if (value === "light" || value === "dark" || value === "system") {
    settings.setTheme(value);
  }
}
</script>

<template>
  <div class="runtime-start" data-testid="runtime-startup-panel">
    <section class="runtime-start__card" aria-labelledby="runtime-start-title">
      <header class="runtime-start__head">
        <YmSpinner v-if="runtime.phase !== 'failed'" :size="22" />
        <h1 id="runtime-start-title" class="runtime-start__title">
          {{ t("runtime.init.title") }}
        </h1>
        <p class="runtime-start__body">{{ t("runtime.init.body") }}</p>
      </header>

      <div class="runtime-start__state" aria-live="polite">
        <span class="runtime-start__state-label">{{ t("runtime.init.status") }}</span>
        <span class="runtime-start__state-value" data-testid="runtime-startup-phase">
          {{ phaseLabel }}
        </span>
      </div>
      <p v-if="reasonLabel" class="runtime-start__reason" data-testid="runtime-startup-reason">
        <bdi>{{ reasonLabel }}</bdi>
      </p>
      <p
        v-if="accessErrorText"
        class="runtime-start__reason runtime-start__reason--error"
        role="alert"
        data-testid="runtime-startup-error"
      >
        <bdi>{{ accessErrorText }}</bdi>
      </p>

      <div class="runtime-start__controls">
        <YmField :label="t('runtime.init.language')">
          <YmSelect
            :model-value="settings.locale"
            :options="languageOptions"
            data-testid="runtime-startup-language"
            @update:model-value="onLocale"
          />
        </YmField>
        <YmField :label="t('runtime.init.theme')">
          <YmSegmented
            :model-value="settings.theme"
            :options="themeOptions"
            @update:model-value="onTheme"
          />
        </YmField>
      </div>

      <YmButton
        v-if="canRetry"
        variant="primary"
        icon="refresh"
        data-testid="runtime-startup-retry"
        :loading="runtime.retrying"
        :disabled="runtime.retrying"
        @click="runtime.retry()"
      >
        {{ t("runtime.retry") }}
      </YmButton>
    </section>
  </div>
</template>

<style scoped>
.runtime-start {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  padding: var(--space-6);
  background: var(--color-bg);
}
.runtime-start__card {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  width: min(520px, 100%);
  padding: var(--space-6);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-md);
}
.runtime-start__head {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--space-2);
}
.runtime-start__title {
  font-size: var(--font-size-xl);
}
.runtime-start__body {
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  line-height: var(--line-height-normal);
}
.runtime-start__state {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  background: var(--color-surface-2);
  border-radius: var(--radius-md);
}
.runtime-start__state-label {
  flex: none;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.runtime-start__state-value {
  font-size: var(--font-size-sm);
  color: var(--color-text);
}
.runtime-start__reason {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  background: var(--color-warning-soft);
  border-radius: var(--radius-md);
  color: var(--color-text);
  font-size: var(--font-size-sm);
  line-height: var(--line-height-normal);
}
.runtime-start__reason--error {
  background: var(--color-error-soft);
}
.runtime-start__controls {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding-top: var(--space-2);
  border-top: 1px solid var(--color-border);
}
</style>
