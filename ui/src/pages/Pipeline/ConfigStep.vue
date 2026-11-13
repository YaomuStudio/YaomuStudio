<script setup lang="ts">
import { computed } from "vue";
import { t } from "@/i18n";
import type { TargetLanguage } from "@/domain/languages";
import type { SelectOption } from "./types";
import LanguageMultiSelect from "@/components/LanguageMultiSelect.vue";
import YmCheckbox from "@/components/ui/YmCheckbox.vue";
import YmField from "@/components/ui/YmField.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmPanel from "@/components/ui/YmPanel.vue";
import YmRadioGroup from "@/components/ui/YmRadioGroup.vue";
import YmSwitch from "@/components/ui/YmSwitch.vue";
import YmTextInput from "@/components/ui/YmTextInput.vue";

const props = defineProps<{
  projectName: string;
  targetLanguages: string[];
  targetOptions: TargetLanguage[];
  reusableAudioName?: string;
  reusableSubtitleName?: string;
  reuseAudio: boolean;
  reuseSubtitle: boolean;
  eraseMode: string;
  validateTimestamps: boolean;
  adjustDuration: boolean;
  bilingual: boolean;
  /** 新建流程不展示复用音频/字幕区；既有 Pipeline 默认保持展示。 */
  showReuse?: boolean;
}>();

const emit = defineEmits<{
  (e: "update:projectName", v: string): void;
  (e: "toggleTarget", code: string): void;
  (e: "selectAllTargets"): void;
  (e: "clearTargets"): void;
  (e: "update:reuseAudio", v: boolean): void;
  (e: "update:reuseSubtitle", v: boolean): void;
  (e: "update:eraseMode", v: string): void;
  (e: "update:validateTimestamps", v: boolean): void;
  (e: "update:adjustDuration", v: boolean): void;
  (e: "update:bilingual", v: boolean): void;
}>();

const eraseOptions = computed<SelectOption[]>(() => [
  { value: "dialog_only", label: t("pipeline.config.eraseDialog") },
  { value: "dialog_and_art", label: t("pipeline.config.eraseArt") },
]);

function onProjectName(v: string): void {
  emit("update:projectName", v);
}
function onReuseAudio(v: boolean): void {
  emit("update:reuseAudio", v);
}
function onReuseSubtitle(v: boolean): void {
  emit("update:reuseSubtitle", v);
}
function onErase(v: string): void {
  emit("update:eraseMode", v);
}
function onValidateTimestamps(v: boolean): void {
  emit("update:validateTimestamps", v);
}
function onAdjustDuration(v: boolean): void {
  emit("update:adjustDuration", v);
}
function onBilingual(v: boolean): void {
  emit("update:bilingual", v);
}
</script>

<template>
  <div class="config-step">
    <YmPanel :title="t('pipeline.config.title')">
      <div class="config-step__body">
        <YmField
          :label="t('pipeline.config.projectName')"
          :hint="t('pipeline.config.projectNameHint', { count: projectName.length })"
        >
          <YmTextInput
            :model-value="projectName"
            :placeholder="t('common.projectName')"
            @update:model-value="onProjectName"
          />
        </YmField>
      </div>
    </YmPanel>

    <YmPanel :title="t('pipeline.config.targetLanguages')">
      <div class="config-step__body">
        <LanguageMultiSelect
          :model-value="targetLanguages"
          :options="targetOptions"
          @toggle="emit('toggleTarget', $event)"
          @select-all="emit('selectAllTargets')"
          @clear="emit('clearTargets')"
        />
      </div>
    </YmPanel>

    <YmPanel v-if="showReuse !== false" :title="t('pipeline.config.reuse')">
      <div class="config-step__body">
        <div class="reuse-item">
          <YmCheckbox
            :model-value="reuseAudio"
            :label="t('pipeline.config.reuseAudio')"
            :disabled="!reusableAudioName"
            @update:model-value="onReuseAudio"
          />
          <span class="reuse-name">{{ reusableAudioName ?? "—" }}</span>
        </div>
        <div class="reuse-item">
          <YmCheckbox
            :model-value="reuseSubtitle"
            :label="t('pipeline.config.reuseSubtitle')"
            :disabled="!reusableSubtitleName"
            @update:model-value="onReuseSubtitle"
          />
          <span class="reuse-name">{{ reusableSubtitleName ?? "—" }}</span>
        </div>
        <p v-if="!reusableAudioName && !reusableSubtitleName" class="pending-note">
          {{ t("pipeline.config.reuseNone") }}
        </p>
      </div>
    </YmPanel>

    <YmPanel :title="t('pipeline.config.erase')">
      <YmRadioGroup
        :model-value="eraseMode"
        :options="eraseOptions"
        @update:model-value="onErase"
      />
    </YmPanel>

    <YmPanel :title="t('pipeline.config.subtitleOptions')">
      <div class="config-step__body">
        <div class="switch-item">
          <YmSwitch
            :model-value="validateTimestamps"
            :label="t('pipeline.config.validateTimestamps')"
            @update:model-value="onValidateTimestamps"
          />
          <p class="switch-hint">{{ t("pipeline.config.validateTimestampsHint") }}</p>
        </div>
        <div class="switch-item" data-testid="adjust-duration-hint" data-status="unavailable">
          <YmSwitch
            :model-value="adjustDuration"
            :label="t('pipeline.config.adjustDuration')"
            disabled
            @update:model-value="onAdjustDuration"
          />
          <p class="switch-hint">{{ t("pipeline.config.adjustDurationHint") }}</p>
        </div>
        <div class="switch-item">
          <YmSwitch
            :model-value="bilingual"
            :label="t('pipeline.config.bilingual')"
            @update:model-value="onBilingual"
          />
          <p class="switch-hint">{{ t("pipeline.config.bilingualHint") }}</p>
        </div>
      </div>
    </YmPanel>

    <YmPanel :title="t('pipeline.config.style')">
      <div class="style-pending">
        <YmIcon name="subtitles" :size="24" />
        <div class="style-pending__text">
          <span class="style-pending__title">{{ t("pipeline.config.stylePending") }}</span>
          <span class="style-pending__hint">{{ t("pipeline.config.styleHint") }}</span>
        </div>
      </div>
    </YmPanel>

    <p class="recalc-hint">{{ t("pipeline.config.recalcHint") }}</p>
  </div>
</template>

<style scoped>
.config-step {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.config-step__body {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.reuse-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.reuse-name {
  padding-left: 26px;
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.switch-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.switch-hint {
  padding-left: 44px;
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.style-pending {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-3);
  border: 1px dashed var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
  color: var(--color-text-faint);
}
.style-pending__text {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.style-pending__title {
  font-size: var(--font-size-md);
  color: var(--color-text-muted);
  font-weight: 500;
}
.style-pending__hint {
  font-size: var(--font-size-xs);
}
.recalc-hint {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
  line-height: var(--line-height-normal);
}
.pending-note {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
</style>
