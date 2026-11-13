<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import {
  detectSystemLocale,
  ensureLocalePack,
  getLocaleCompletion,
  hasLocalePack,
  t,
  type Locale,
} from "@/i18n";
import { messageNotice, renderNotice, type SafeNotice } from "@/i18n/notice";
import { UI_LANGUAGES } from "@/domain/languages";
import PageHeader from "@/components/layout/PageHeader.vue";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmField from "@/components/ui/YmField.vue";
import YmNotice from "@/components/ui/YmNotice.vue";
import YmProgress from "@/components/ui/YmProgress.vue";
import YmSelect from "@/components/ui/YmSelect.vue";
import YmSwitch from "@/components/ui/YmSwitch.vue";
import { useSettingsStore } from "@/stores/settings";

const FOLLOW_KEY = "yaomu.settings.followSystem";

const settings = useSettingsStore();
const notice = ref<SafeNotice | null>(null);
const noticeText = computed(() => renderNotice(notice.value));

/** 未显式选择过语言时默认跟随系统；手动选择会写入 "0"。 */
const followSystem = ref(localStorage.getItem(FOLLOW_KEY) !== "0");

const systemCode = (): string => (navigator.language || "en").toLowerCase();

const detectedSystemName = computed(() => {
  const code = systemCode();
  const exact = UI_LANGUAGES.find((l) => l.code.toLowerCase() === code);
  if (exact) return exact.nativeName ?? exact.name;
  const base = code.split("-")[0] ?? code;
  const partial = UI_LANGUAGES.find((l) => l.code.toLowerCase() === base);
  return partial?.nativeName ?? partial?.name ?? navigator.language;
});

const current = computed(() => UI_LANGUAGES.find((l) => l.code === settings.locale));
const currentCompletion = computed(() =>
  current.value ? getLocaleCompletion(current.value.code) : 0,
);

const options = computed(() =>
  UI_LANGUAGES.map((l) => ({
    value: l.code,
    label: l.nativeName ?? l.name,
  })),
);

/** 切换语言并等待语言包加载，随后按实际完成度给出可读提示。 */
async function applyLocale(value: Locale): Promise<void> {
  settings.setLocaleAndApply(value);
  await ensureLocalePack(value);
  // 语言包加载期间用户可能又切换到别的语言；只为仍然生效的语言写提示。
  if (settings.locale !== value) return;
  const completion = getLocaleCompletion(value);
  if (completion === 0) notice.value = messageNotice("settings.language.packMissing");
  else if (completion < 100) notice.value = messageNotice("settings.language.packPartial");
  else notice.value = null;
}

function applyFollowSystem(): void {
  void applyLocale(detectSystemLocale());
}

function onLocale(value: string): void {
  followSystem.value = false;
  localStorage.setItem(FOLLOW_KEY, "0");
  void applyLocale(value);
}

function onFollowSystem(value: boolean): void {
  followSystem.value = value;
  localStorage.setItem(FOLLOW_KEY, value ? "1" : "0");
  notice.value = null;
  if (value) applyFollowSystem();
}

onMounted(() => {
  // 预加载已有语言包，使列表里的完成度基于真实键数而非硬编码值。
  const existing = UI_LANGUAGES.filter((l) => hasLocalePack(l.code)).map((l) =>
    ensureLocalePack(l.code),
  );
  void Promise.all(existing).then(() => {
    if (!followSystem.value) return;
    localStorage.setItem(FOLLOW_KEY, "1");
    applyFollowSystem();
  });
});
</script>

<template>
  <div class="page">
    <PageHeader
      :title="t('settings.language.title')"
      :description="t('settings.language.desc')"
      icon="globe"
    />

    <YmCard>
      <div class="stack">
        <div class="switch-row">
          <div class="switch-row__text">
            <p class="switch-row__label">{{ t("settings.language.followSystem") }}</p>
            <p class="switch-row__hint">{{ t("settings.language.followSystemHint") }}</p>
            <p class="switch-row__meta">
              {{ t("settings.language.systemDetected", { language: detectedSystemName }) }}
            </p>
          </div>
          <YmSwitch
            :aria-label="t('settings.language.followSystem')"
            :model-value="followSystem"
            @update:model-value="onFollowSystem"
          />
        </div>

        <YmField :label="t('settings.language.current')" :hint="t('settings.language.listNote')">
          <YmSelect
            :model-value="settings.locale"
            :options="options"
            @update:model-value="onLocale"
          />
        </YmField>

        <YmNotice v-if="noticeText" tone="warning" role="alert"><bdi>{{ noticeText }}</bdi></YmNotice>

        <div v-if="current" class="current-card">
          <div class="current-card__head">
            <div>
              <p class="current-card__name">{{ current.nativeName ?? current.name }}</p>
              <p class="current-card__code mono">{{ current.code }}</p>
            </div>
            <div class="current-card__badges">
              <YmBadge v-if="currentCompletion < 100" tone="warning">
                {{ t("settings.language.completion", { percent: currentCompletion }) }}
              </YmBadge>
              <YmBadge v-if="current.rtl" tone="info">{{ t("settings.language.rtl") }}</YmBadge>
            </div>
          </div>
          <YmProgress v-if="currentCompletion < 100" :value="currentCompletion" tone="warning" />
        </div>
      </div>
    </YmCard>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  max-width: 1240px;
}
.page__title {
  font-size: var(--font-size-xl);
}
.page__desc {
  margin-top: var(--space-1);
  color: var(--color-text-muted);
}
.stack {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
}
.switch-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-5);
}
.switch-row__text {
  min-width: 0;
}
.switch-row__label {
  font-size: var(--font-size-sm);
  font-weight: 500;
  color: var(--color-text-muted);
}
.switch-row__hint {
  margin-top: 2px;
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.switch-row__meta {
  margin-top: var(--space-1);
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
}
.alert {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-warning);
  border-radius: var(--radius-md);
  background: var(--color-warning-soft);
  color: var(--color-text);
  font-size: var(--font-size-sm);
}
.current-card {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
}
.current-card__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-4);
}
.current-card__name {
  font-size: var(--font-size-lg);
  font-weight: 600;
}
.current-card__code {
  margin-top: 2px;
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.current-card__badges {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: var(--space-1);
}
</style>
