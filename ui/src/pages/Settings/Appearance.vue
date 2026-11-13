<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { formatPercent, t } from "@/i18n";
import PageHeader from "@/components/layout/PageHeader.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmSegmented from "@/components/ui/YmSegmented.vue";
import { useSettingsStore } from "@/stores/settings";

const settings = useSettingsStore();
// 拖动期间只更新草稿，松开后才应用缩放；否则滑块自身随页面缩放移动，
// 指针与刻度错位会形成自反馈，造成缩放值来回漂移。
const scaleDraft = ref(settings.uiScale);

watch(
  () => settings.uiScale,
  (value) => {
    scaleDraft.value = value;
  },
);

const themeOptions = computed(() => [
  { value: "light", label: t("settings.appearance.theme.light") },
  { value: "dark", label: t("settings.appearance.theme.dark") },
  { value: "system", label: t("settings.appearance.theme.system") },
]);

/** 刻度按 80–150 的真实比例定位；等距排布会和滑块实际位置错开。 */
const SCALE_MIN = 80;
const SCALE_MAX = 150;
const scaleMarks = [80, 100, 125, 150].map((value) => ({
  value,
  percent: ((value - SCALE_MIN) / (SCALE_MAX - SCALE_MIN)) * 100,
}));

function setTheme(value: string): void {
  if (value === "light" || value === "dark" || value === "system") settings.setTheme(value);
}

function onScaleInput(event: Event): void {
  const value = Number((event.target as HTMLInputElement).value);
  if (Number.isFinite(value)) scaleDraft.value = value;
}

function commitScale(): void {
  settings.setUiScale(scaleDraft.value);
}

function resetScale(): void {
  scaleDraft.value = 100;
  settings.setUiScale(100);
}
</script>

<template>
  <div class="page">
    <PageHeader
      :title="t('settings.appearance.title')"
      :description="t('settings.appearance.desc')"
      icon="moon"
    />

    <YmCard>
      <div class="appearance">
        <div class="appearance__main">
          <section class="setting-block">
            <div class="setting-block__head">
              <div>
                <h2 class="setting-block__title">{{ t("settings.appearance.theme") }}</h2>
                <p class="setting-block__hint">{{ t("settings.appearance.themeHint") }}</p>
              </div>
            </div>
            <YmSegmented
              class="theme-switch"
              :model-value="settings.theme"
              :options="themeOptions"
              @update:model-value="setTheme"
            />
          </section>

          <section class="setting-block">
            <div class="setting-block__head">
              <div>
                <h2 class="setting-block__title">{{ t("settings.appearance.scale") }}</h2>
                <p class="setting-block__hint">{{ t("settings.appearance.scaleHint") }}</p>
              </div>
              <span class="scale-value mono">{{ formatPercent(scaleDraft) }}</span>
            </div>
            <div class="scale-control">
              <input
                class="scale-range"
                type="range"
                :min="SCALE_MIN"
                :max="SCALE_MAX"
                step="5"
                :value="scaleDraft"
                :aria-label="t('settings.appearance.scale')"
                @input="onScaleInput"
                @change="commitScale"
              />
              <div class="scale-marks" aria-hidden="true">
                <span
                  v-for="mark in scaleMarks"
                  :key="mark.value"
                  :style="{ insetInlineStart: `${mark.percent}%` }"
                ></span>
              </div>
              <div class="scale-ticks mono" aria-hidden="true">
                <span
                  v-for="mark in scaleMarks"
                  :key="mark.value"
                  :style="{ insetInlineStart: `${mark.percent}%` }"
                >{{ mark.value }}%</span>
              </div>
            </div>
            <div class="setting-block__actions">
              <YmButton size="sm" variant="ghost" icon="refresh" @click="resetScale">
                {{ t("settings.appearance.reset") }}
              </YmButton>
            </div>
          </section>
        </div>

        <aside class="preview" :aria-label="t('settings.appearance.preview')">
          <div class="preview__head">
            <span>{{ t("settings.appearance.preview") }}</span>
            <span class="preview__badge" aria-hidden="true">UI</span>
          </div>
          <div class="preview__window">
            <div class="preview__bar" aria-hidden="true">
              <span></span><span></span><span></span>
            </div>
            <div class="preview__body">
              <div class="preview__sidebar" aria-hidden="true">
                <i class="is-active"></i>
                <i></i>
                <i></i>
              </div>
              <div class="preview__content">
                <span class="preview__label">{{ t("settings.appearance.previewTitle") }}</span>
                <span class="preview__text">{{ t("settings.appearance.previewBody") }}</span>
                <div class="preview__cards" aria-hidden="true">
                  <i></i><i></i>
                </div>
              </div>
            </div>
          </div>
          <p class="preview__hint">{{ t("settings.appearance.previewHint") }}</p>
        </aside>
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
.appearance {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(280px, 380px);
  gap: 32px;
  align-items: start;
}
.appearance__main {
  display: flex;
  flex-direction: column;
  gap: 28px;
  min-width: 0;
}
.setting-block {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.setting-block + .setting-block {
  padding-block-start: 24px;
  border-block-start: 1px solid var(--color-border);
}
.setting-block__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.setting-block__title {
  margin: 0;
  color: var(--color-text);
  font-size: var(--font-size-md);
  font-weight: 600;
}
.setting-block__hint {
  margin-block-start: 3px;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
  line-height: 1.55;
}
.theme-switch {
  width: min(420px, 100%);
}
.scale-value {
  flex: none;
  color: var(--color-primary);
  font-size: var(--font-size-lg);
  font-weight: 600;
}
.scale-control {
  /* 全局滑块圆点 18px（styles/widgets.css）；刻度两侧各让出半个圆点，
     使刻度与圆点中心、以及 80–150 的数值位置一致。数值刻度固定 LTR。 */
  --scale-thumb: 18px;
  direction: ltr;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.scale-range {
  width: 100%;
  height: 20px;
  margin: 0;
  accent-color: var(--color-primary);
}
.scale-marks,
.scale-ticks {
  position: relative;
  margin-inline: calc(var(--scale-thumb) / 2);
}
.scale-marks {
  height: 5px;
  pointer-events: none;
}
.scale-marks span {
  position: absolute;
  top: 0;
  width: 1px;
  height: 5px;
  background: var(--color-border-strong);
  transform: translateX(-50%);
}
.scale-ticks {
  height: 16px;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.scale-ticks span {
  position: absolute;
  top: 0;
  transform: translateX(-50%);
  white-space: nowrap;
}
.setting-block__actions {
  display: flex;
  justify-content: flex-end;
}
.preview {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 16px;
  border: 1px solid var(--color-border);
  border-radius: 12px;
  background: var(--color-surface-2);
}
.preview__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  font-weight: 500;
}
.preview__badge {
  padding: 1px 6px;
  border-radius: var(--radius-pill);
  background: var(--color-surface);
  color: var(--color-text-faint);
  font-size: 10px;
}
.preview__window {
  overflow: hidden;
  border: 1px solid var(--color-border);
  border-radius: 10px;
  background: var(--color-surface);
  box-shadow: var(--shadow-sm);
}
.preview__bar {
  display: flex;
  gap: 4px;
  padding: 8px 10px;
  border-block-end: 1px solid var(--color-border);
  background: var(--color-surface-2);
}
.preview__bar span {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-border-strong);
}
.preview__body {
  display: flex;
  min-height: 148px;
}
.preview__sidebar {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 52px;
  flex: none;
  padding: 12px 10px;
  border-inline-end: 1px solid var(--color-border);
}
.preview__sidebar i {
  display: block;
  height: 8px;
  border-radius: 4px;
  background: var(--color-border);
}
.preview__sidebar i.is-active {
  background: var(--color-primary);
}
.preview__content {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
  padding: 14px;
}
.preview__label {
  color: var(--color-text);
  font-size: var(--font-size-sm);
  font-weight: 600;
}
.preview__text {
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
  line-height: 1.55;
}
.preview__cards {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 6px;
  margin-block-start: 4px;
}
.preview__cards i {
  height: 30px;
  border-radius: 6px;
  background: var(--color-primary-soft);
}
.preview__cards i + i {
  background: var(--color-surface-2);
}
.preview__hint {
  margin: 0;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
  line-height: 1.5;
}

/*
 * Settings 内容右侧固定导航占 220px；当窗口不足以容纳设置列 + 预览列时
 * （展开侧栏的常见窄窗），预览改为在设置下方堆叠，避免主操作被挤成窄条。
 */
@media (max-width: 1180px) {
  .appearance {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
