<script setup lang="ts">
import { computed, ref } from "vue";
import { t, formatBytes, formatDuration } from "@/i18n";
import type { Asset, AssetStatus, AssetType } from "@/domain/types";
import type { Tone } from "@/domain/status";
import type { SelectOption } from "./types";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmEmpty from "@/components/ui/YmEmpty.vue";
import YmDropzone from "@/components/ui/YmDropzone.vue";
import YmField from "@/components/ui/YmField.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmPanel from "@/components/ui/YmPanel.vue";
import YmSegmented from "@/components/ui/YmSegmented.vue";
import YmSelect from "@/components/ui/YmSelect.vue";
import YmSwitch from "@/components/ui/YmSwitch.vue";

const props = defineProps<{
  assets: Asset[];
  /** 当前源素材 Asset.id；为空表示尚未选择/无候选。 */
  sourceAssetId: string;
  /** 可作为源素材的 Asset.id（业务规则由页面统一计算）。 */
  sourceAssetIds: string[];
  sourceLanguage: string;
  sourceOptions: SelectOption[];
  sourceHint: string;
  extractMode: string;
  ocrLanguage: string;
  ocrOptions: SelectOption[];
  copyToProject: boolean;
  importing: boolean;
  /** Electron 桥可用时点击交给主进程 dialog；sandbox 下拿不到 input File 的真实路径。 */
  useShellPicker: boolean;
}>();

const emit = defineEmits<{
  (e: "update:sourceLanguage", v: string): void;
  (e: "update:extractMode", v: string): void;
  (e: "update:ocrLanguage", v: string): void;
  (e: "update:copyToProject", v: boolean): void;
  (e: "selectSource", id: string): void;
  (e: "pick"): void;
  (e: "pickFolder"): void;
  (e: "files", files: File[]): void;
  /** 外部源文件缺失时由页面重新选择同内容文件。 */
  (e: "relocate", assetId: string): void;
}>();

const fileInput = ref<HTMLInputElement | null>(null);

const extractOptions = computed<SelectOption[]>(() => [
  { value: "asr", label: t("pipeline.import.mode.asr") },
  { value: "ocr", label: t("pipeline.import.mode.ocr") },
  { value: "both", label: t("pipeline.import.mode.both") },
]);

const ASSET_TYPE_KEYS: Record<AssetType, string> = {
  video: "pipeline.assetType.video",
  audio: "pipeline.assetType.audio",
  subtitle: "pipeline.assetType.subtitle",
  audio_track: "pipeline.assetType.audio_track",
  image: "pipeline.assetType.image",
  archive: "pipeline.assetType.archive",
  other: "pipeline.assetType.other",
};

const ASSET_STATUS_KEYS: Record<AssetStatus, string> = {
  importing: "pipeline.assetStatus.importing",
  ready: "pipeline.assetStatus.ready",
  missing: "pipeline.assetStatus.missing",
  corrupt: "pipeline.assetStatus.corrupt",
};

const ASSET_STATUS_TONE: Record<AssetStatus, Tone> = {
  importing: "info",
  ready: "success",
  missing: "warning",
  corrupt: "error",
};

function assetIcon(type: AssetType): string {
  switch (type) {
    case "video":
      return "film";
    case "audio":
    case "audio_track":
      return "wave";
    case "subtitle":
      return "subtitles";
    default:
      return "box";
  }
}

function isSourceCandidate(asset: Asset): boolean {
  return props.sourceAssetIds.includes(asset.id);
}

const sourceCandidates = computed(() => props.assets.filter(isSourceCandidate));

function selectSource(asset: Asset): void {
  if (isSourceCandidate(asset)) emit("selectSource", asset.id);
}

/** 只处理缺失的外部引用；copy/generated 资产由 Core 拒绝，页面不额外判断来源。 */
function relocateAsset(asset: Asset): void {
  emit("relocate", asset.id);
}

function onAssetKey(event: KeyboardEvent, asset: Asset): void {
  if (!isSourceCandidate(asset)) return;
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    selectSource(asset);
  }
}

function assetSubline(asset: Asset): string {
  const parts = [t(ASSET_TYPE_KEYS[asset.type])];
  if (asset.meta?.durationMs) parts.push(formatDuration(asset.meta.durationMs));
  if (asset.meta?.width && asset.meta.height) parts.push(`${asset.meta.width}×${asset.meta.height}`);
  parts.push(formatBytes(asset.sizeBytes));
  return parts.join(" · ");
}

function pickFiles(): void {
  if (props.useShellPicker) emit("pick");
}

/** 文件夹入口只在桌面桥可用时出现：目录授权与递归扫描都在主进程完成。 */
function pickFolder(): void {
  if (!props.useShellPicker || props.importing) return;
  emit("pickFolder");
}

function onFiles(files: File[]): void {
  if (files.length > 0) emit("files", files);
}

function onSource(v: string): void {
  emit("update:sourceLanguage", v);
}

function onMode(v: string): void {
  emit("update:extractMode", v);
}

function onOcr(v: string): void {
  emit("update:ocrLanguage", v);
}

function onCopy(v: boolean): void {
  emit("update:copyToProject", v);
}
</script>

<template>
  <div class="import-step">
    <YmPanel :title="t('pipeline.import.title')">
      <div class="import-step__body">
        <YmDropzone
          :manual="useShellPicker"
          multiple
          :title="t('pipeline.import.dropTitle')"
          :hint="t('pipeline.import.dropHint')"
          :disabled="importing"
          @pick="pickFiles"
          @files="onFiles"
        >
          <YmButton
            v-if="importing"
            size="sm"
            loading
            :disabled="true"
            @click.stop
          >
            {{ t("pipeline.import.importing") }}
          </YmButton>
        </YmDropzone>
        <div v-if="useShellPicker" class="import-step__picker">
          <YmButton
            size="sm"
            variant="secondary"
            icon="folder"
            :disabled="importing"
            @click="pickFolder"
          >
            {{ t("pipeline.import.selectFolder") }}
          </YmButton>
          <span class="import-step__picker-hint">{{ t("pipeline.import.folderHint") }}</span>
        </div>
        <YmSwitch
          :model-value="copyToProject"
          :label="t('pipeline.import.copy')"
          @update:model-value="onCopy"
        />
      </div>
    </YmPanel>

    <YmPanel :title="t('pipeline.import.assets')">
      <div v-if="assets.length === 0" class="import-step__empty">
        <YmEmpty
          icon="film"
          :title="t('pipeline.import.noAssetsTitle')"
          :description="t('pipeline.import.noAssetsDesc')"
        />
      </div>
      <ul v-else class="asset-list">
        <li
          v-for="asset in assets"
          :key="asset.id"
          class="asset"
          :class="{
            'asset--selectable': isSourceCandidate(asset),
            'asset--selected': asset.id === sourceAssetId,
          }"
          :data-asset-id="asset.id"
          :role="isSourceCandidate(asset) ? 'button' : undefined"
          :tabindex="isSourceCandidate(asset) ? 0 : undefined"
          :aria-pressed="isSourceCandidate(asset) ? asset.id === sourceAssetId : undefined"
          @click="selectSource(asset)"
          @keydown="onAssetKey($event, asset)"
        >
          <YmIcon class="asset__icon" :name="assetIcon(asset.type)" :size="18" />
          <div class="asset__main">
            <span class="asset__name" :title="asset.name">{{ asset.name }}</span>
            <span class="asset__meta">{{ assetSubline(asset) }}</span>
          </div>
          <YmBadge
            v-if="asset.id === sourceAssetId && isSourceCandidate(asset)"
            tone="accent"
          >
            {{ t("pipeline.import.sourceBadge") }}
          </YmBadge>
          <YmButton
            v-if="asset.status === 'missing' && !importing"
            size="sm"
            variant="secondary"
            icon="folder"
            :title="t('pipeline.import.relocateHint')"
            @click.stop="relocateAsset(asset)"
            @keydown.stop
          >
            {{ t("pipeline.import.relocate") }}
          </YmButton>
          <YmBadge :tone="ASSET_STATUS_TONE[asset.status]">
            {{ t(ASSET_STATUS_KEYS[asset.status]) }}
          </YmBadge>
        </li>
      </ul>
      <p v-if="assets.length > 0 && sourceCandidates.length === 0" class="source-hint source-hint--warning">
        {{ t("pipeline.import.sourceMissing") }}
      </p>
      <p v-else-if="assets.length > 0" class="source-hint">
        {{ t("pipeline.import.sourceHint") }}
      </p>
    </YmPanel>

    <YmPanel :title="t('pipeline.import.extractTitle')">
      <div class="import-step__body">
        <YmField :label="t('pipeline.import.source')" :hint="sourceHint">
          <YmSelect
            :model-value="sourceLanguage"
            :options="sourceOptions"
            @update:model-value="onSource"
          />
        </YmField>

        <YmField :label="t('pipeline.import.extractMode')">
          <YmSegmented
            :model-value="extractMode"
            :options="extractOptions"
            @update:model-value="onMode"
          />
        </YmField>

        <YmField
          v-if="extractMode !== 'asr'"
          :label="t('pipeline.import.ocrLanguage')"
        >
          <YmSelect
            :model-value="ocrLanguage"
            :options="ocrOptions"
            @update:model-value="onOcr"
          />
        </YmField>
      </div>
    </YmPanel>
  </div>
</template>

<style scoped>
.import-step {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.import-step__body {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.import-step__empty {
  padding: var(--space-2);
}
.import-step__picker {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.import-step__picker-hint {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.asset-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  margin: 0;
  padding: 0;
  list-style: none;
}
.asset {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2);
  border-radius: var(--radius-sm);
}
.asset:hover {
  background: var(--color-surface-2);
}
.asset--selectable {
  cursor: pointer;
}
.asset--selectable:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 1px;
}
.asset--selected {
  background: var(--color-primary-soft);
  box-shadow: inset 2px 0 0 var(--color-primary);
}
.source-hint {
  margin: var(--space-2) 0 0;
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.source-hint--warning {
  color: var(--color-warning-strong);
}
.asset__icon {
  color: var(--color-text-muted);
  flex: none;
}
.asset__main {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
  flex: 1;
}
.asset__name {
  font-size: var(--font-size-md);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.asset__meta {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
</style>
