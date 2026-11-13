<script setup lang="ts">
import { computed } from "vue";
import { t } from "@/i18n";
import type { ExportKind, ExportRecord } from "@/domain/types";
import { taskStatusLabelKey, taskStatusTone } from "@/domain/status";
import type { SelectOption } from "./types";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCheckbox from "@/components/ui/YmCheckbox.vue";
import YmField from "@/components/ui/YmField.vue";
import YmSegmented from "@/components/ui/YmSegmented.vue";
import YmSelect from "@/components/ui/YmSelect.vue";
import YmTextInput from "@/components/ui/YmTextInput.vue";

const props = defineProps<{
  kind: ExportKind;
  language: string;
  languageOptions: SelectOption[];
  outputDir: string;
  namingTemplate: string;
  namingPreview: string;
  formatLabel: string;
  exporting: boolean;
  record: ExportRecord | null;
  canExport: boolean;
  blockedHint: string;
  bilingual: boolean;
  bilingualAvailable: boolean;
}>();

const emit = defineEmits<{
  (e: "update:kind", v: string): void;
  (e: "update:language", v: string): void;
  (e: "update:namingTemplate", v: string): void;
  (e: "update:bilingual", v: boolean): void;
  (e: "export"): void;
  (e: "openFolder"): void;
}>();

const kindOptions = computed<SelectOption[]>(() => [
  { value: "video", label: t("pipeline.export.kindVideo") },
  { value: "audio", label: t("pipeline.export.kindAudio") },
  { value: "subtitle", label: t("pipeline.export.kindSubtitle") },
]);

const recordLabel = computed(() =>
  props.record ? t(taskStatusLabelKey(props.record.status)) : "",
);
const recordTone = computed(() => (props.record ? taskStatusTone(props.record.status) : "neutral"));
const recordPath = computed(() => props.record?.outputPath ?? "—");
const folderReady = computed(() => props.record?.status === "SUCCEEDED");

function onKind(v: string): void {
  emit("update:kind", v);
}
function onLanguage(v: string): void {
  emit("update:language", v);
}
function onNaming(v: string): void {
  emit("update:namingTemplate", v);
}
function onBilingual(v: boolean): void {
  emit("update:bilingual", v);
}
</script>

<template>
  <section class="export">
    <header class="export__head">
      <h2 class="export__title">{{ t("pipeline.export.title") }}</h2>
    </header>

    <YmField :label="t('pipeline.export.kind')">
      <YmSegmented :model-value="kind" :options="kindOptions" @update:model-value="onKind" />
    </YmField>

    <YmField v-if="languageOptions.length > 1" :label="t('pipeline.export.language')">
      <YmSelect
        :model-value="language"
        :options="languageOptions"
        @update:model-value="onLanguage"
      />
    </YmField>

    <div class="export__format">
      <span class="export__format-label">{{ t("pipeline.export.format") }}</span>
      <span class="export__format-value">{{ formatLabel }}</span>
    </div>
    <p class="pending-note">{{ t("pipeline.export.formatPending") }}</p>

    <YmField v-if="kind === 'subtitle'" :label="t('pipeline.export.bilingual')">
      <div class="export__bilingual">
        <YmCheckbox
          :model-value="bilingual"
          :disabled="!bilingualAvailable"
          :label="t('pipeline.export.bilingualLabel')"
          @update:model-value="onBilingual"
        />
        <p v-if="!bilingualAvailable" class="export__hint">
          {{ t("pipeline.export.bilingualUnavailable") }}
        </p>
      </div>
    </YmField>

    <YmField
      :label="t('pipeline.export.outputDir')"
      :hint="t('pipeline.export.outputDirHint')"
    >
      <YmTextInput :model-value="outputDir" disabled />
    </YmField>

    <YmField
      :label="t('pipeline.export.naming')"
      :hint="t('pipeline.export.namingHint')"
    >
      <YmTextInput :model-value="namingTemplate" @update:model-value="onNaming" />
    </YmField>

    <div class="export__preview">
      <span class="export__preview-label">{{ t("pipeline.export.preview") }}</span>
      <code class="export__preview-value mono">{{ namingPreview }}</code>
    </div>

    <p v-if="!canExport && blockedHint" class="export__blocked">{{ blockedHint }}</p>

    <YmButton
      variant="primary"
      size="md"
      icon="download"
      :loading="exporting"
      :disabled="!canExport || exporting"
      @click="emit('export')"
    >
      {{ t("pipeline.export.start") }}
    </YmButton>

    <div v-if="record" class="export__record">
      <div class="export__record-head">
        <span class="export__record-title">{{ t("pipeline.export.latest") }}</span>
        <YmBadge :tone="recordTone">{{ recordLabel }}</YmBadge>
      </div>
      <p class="export__record-line">
        <span>{{ t("pipeline.export.outputPath") }}</span>
        <code class="mono">{{ recordPath }}</code>
      </p>
      <div class="export__record-actions">
        <span class="export__record-id mono">{{ record.id }}</span>
        <YmButton
          variant="secondary"
          size="sm"
          icon="folder-open"
          :disabled="!folderReady"
          :title="folderReady ? undefined : t('pipeline.export.openFolderPending')"
          @click="emit('openFolder')"
        >
          {{ t("pipeline.export.openFolder") }}
        </YmButton>
      </div>
    </div>
  </section>
</template>

<style scoped>
.export {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-4);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
}
.export__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.export__title {
  font-size: var(--font-size-lg);
}
.export__format {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.export__format-label {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.export__format-value {
  font-size: var(--font-size-sm);
  color: var(--color-text);
  font-weight: 500;
}
.export__bilingual {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}
.export__hint {
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.export__preview {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  background: var(--color-surface-2);
  border-radius: var(--radius-sm);
  min-width: 0;
}
.export__preview-label {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
  flex: none;
}
.export__preview-value {
  font-size: var(--font-size-sm);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.export__blocked {
  font-size: var(--font-size-xs);
  color: var(--color-warning-strong);
}
.export__record {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding-top: var(--space-3);
  border-top: 1px solid var(--color-border);
}
.export__record-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.export__record-title {
  font-size: var(--font-size-sm);
  font-weight: 500;
}
.export__record-line {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
  min-width: 0;
}
.export__record-line code {
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.export__record-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}
.export__record-id {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.pending-note {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
</style>
