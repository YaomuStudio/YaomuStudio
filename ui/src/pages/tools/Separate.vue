<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { formatBytes, t } from "@/i18n";
import { api } from "@/api";
import { errorText, preflightIssueKey } from "@/i18n/errorDisplay";
import { messageNotice, renderNotice, type SafeNotice } from "@/i18n/notice";
import type { Asset, Capability, CapabilitySlotKey } from "@/domain/types";
import {
  nodeStatusLabelKey,
  nodeStatusTone,
  runStatusLabelKey,
  runStatusTone,
  slotStateLabelKey,
  slotStateTone,
} from "@/domain/status";
import { useCapabilitiesStore } from "@/stores/capabilities";
import { useCreateGuard } from "@/composables/useCreateGuard";
import { useCreationFlow } from "@/composables/useCreationFlow";
import { evaluateAccess } from "@/domain/capabilityAccess";
import WorkbenchPage from "@/components/layout/WorkbenchPage.vue";
import CapabilityAccessNotice from "@/components/layout/CapabilityAccessNotice.vue";
import CreationNotice from "@/components/tools/CreationNotice.vue";
import ExecutionLogPanel from "@/components/tools/ExecutionLogPanel.vue";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmDropzone from "@/components/ui/YmDropzone.vue";
import YmEmpty from "@/components/ui/YmEmpty.vue";
import YmFileRow from "@/components/ui/YmFileRow.vue";
import YmField from "@/components/ui/YmField.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmIconButton from "@/components/ui/YmIconButton.vue";
import YmPanel from "@/components/ui/YmPanel.vue";
import YmTextInput from "@/components/ui/YmTextInput.vue";
import { loadNodeArtifacts, useToolRun } from "./useToolRun";
import { useRunTask } from "./useRunTask";
import { useMediaPreview } from "@/composables/useMediaPreview";

interface PickedFile {
  name: string;
  sizeBytes: number;
}

const desktopApi = typeof window !== "undefined" ? window.yaomu?.api : undefined;
const caps = useCapabilitiesStore();

/** 所需引擎未就绪时禁用创建，并在页脚说明缺少的能力。 */
const { blocked: createBlocked, blockedText: createBlockedText } = useCreateGuard("tool-separate");

/** 连续创建：清空表单、结果与当前 Run 视图，保留本工具的配置项。 */
function resetForm(): void {
  projectName.value = "";
  mediaFile.value = null;
  media.value = null;
  showErrors.value = false;
  mediaPathError.value = "";
  resultError.value = "";
  resultAssets.value = [];
  saveBusy.value = "";
  saveMessage.value = null;
  saveError.value = null;
  resetRun();
}

const { hasProject, canOpenProject, primaryLabel, onPrimary, openProject, startNew } = useCreationFlow({
  projectId: () => currentRun.value?.projectId,
  busy: () => runBusy.value,
  create,
  reset: resetForm,
});

const projectName = ref("");
const mediaFile = ref<File | null>(null);
const media = ref<PickedFile | null>(null);
const { kind: mediaKind, url: mediaPreviewUrl } = useMediaPreview(mediaFile);
const showErrors = ref(false);
const mediaPathError = ref("");
const resultError = ref("");
const resultAssets = ref<Asset[]>([]);
const saveBusy = ref("");
const saveMessage = ref<SafeNotice | null>(null);
const saveError = ref<SafeNotice | null>(null);
const saveMessageText = computed(() => renderNotice(saveMessage.value));
const saveErrorText = computed(() => renderNotice(saveError.value));

const relatedSlotKeys: CapabilitySlotKey[] = ["separation", "ffmpeg"];
const relatedCapabilities = computed<Capability[]>(() =>
  relatedSlotKeys
    .map((slot) => caps.slotState(slot))
    .filter((cap): cap is Capability => cap !== undefined),
);

const {
  busy: runBusy,
  phase: runPhase,
  run: currentRun,
  issues: runIssues,
  error: runError,
  partial: runPartial,
  start: startRun,
  restore: restoreRun,
  reset: resetRun,
} = useToolRun();

const projectError = computed(() =>
  showErrors.value && !projectName.value.trim() ? t("tools.common.projectNameErr") : "",
);
const mediaError = computed(() =>
  showErrors.value && !media.value ? t("tools.separate.media.err") : "",
);
const mediaIcon = computed(() => {
  const name = media.value?.name.toLowerCase() ?? "";
  return [".mp4", ".mov", ".mkv", ".m3u8"].some((ext) => name.endsWith(ext)) ? "film" : "wave";
});
/** 只展示实际执行的节点；任务在等执行器时节点也显示等待资源，不能谎称运行中。 */
const runNodeRows = computed(() =>
  (currentRun.value?.nodes ?? [])
    .filter((node) => node.status !== "skipped")
    .map((node) => {
      const waiting = node.status === "running" && separateTask.value?.status === "WAITING_RESOURCE";
      return {
        key: node.key,
        label: t(`pipeline.node.${node.key}`),
        status: node.status,
        waiting,
        progressText: waiting
          ? "0%"
          : node.status === "running" && node.progress === 0
            ? t("tools.common.nodeWorking")
            : `${node.progress}%`,
      };
    }),
);
const runPhaseLabel = computed(() => t(`tools.flow.${runPhase.value}`));
const footerStatus = computed(() => {
  if (separateTask.value?.status === "WAITING_RESOURCE") return t("state.waitingResource");
  if (runBusy.value) return t("creation.footer.running");
  if (createBlocked.value) return createBlockedText.value;
  const missing: string[] = [];
  if (!projectName.value.trim()) missing.push(t("tools.common.projectName"));
  if (!media.value) missing.push(t("tools.separate.media.label"));
  return missing.length
    ? t("creation.footer.missing", { items: missing.join(" · ") })
    : t("creation.footer.ready");
});
const separateTask = useRunTask(
  () => currentRun.value?.id,
  "03_separate",
  () => runBusy.value,
);

function resolveFilePath(file: File): string {
  if (desktopApi) {
    return desktopApi.getPathForFile(file).trim();
  }
  const legacy = (file as File & { path?: string }).path;
  return (legacy ?? file.name).trim();
}

function onMedia(file: File | undefined): void {
  if (!file) return;
  mediaFile.value = file;
  media.value = { name: file.name, sizeBytes: file.size };
  mediaPathError.value = "";
}

function removeMedia(): void {
  mediaFile.value = null;
  media.value = null;
  mediaPathError.value = "";
}

async function create(): Promise<void> {
  showErrors.value = true;
  resultError.value = "";
  saveMessage.value = null;
  saveError.value = null;
  resultAssets.value = [];
  if (!projectName.value.trim() || !mediaFile.value) return;
  const filePath = resolveFilePath(mediaFile.value);
  if (!filePath) {
    mediaPathError.value = t("tools.separate.pathUnavailable");
    return;
  }
  showErrors.value = false;
  const result = await startRun({
    projectName: projectName.value.trim(),
    filePath,
    payload: {
      template: "tool_separate",
      sourceLanguage: "und",
      targetLanguages: [],
    },
  });
  if (!result) return;
  try {
    resultAssets.value = await loadNodeArtifacts(result.run.id, result.project.id, "03_separate");
  } catch (err) {
    resultError.value = errorText(err);
    return;
  }
  if (resultAssets.value.length === 0) {
    resultError.value = t("tools.separate.result.missing");
  }
}

async function saveAsset(asset: Asset): Promise<void> {
  saveBusy.value = asset.id;
  saveError.value = null;
  saveMessage.value = null;
  try {
    const saved = await api.saveAsset({
      assetId: asset.id,
      suggestedName: asset.name,
      title: t("tools.separate.save.dialogTitle"),
    });
    if (!saved) return;
    saveMessage.value = messageNotice("tools.separate.save.saved", { path: saved.path });
  } catch {
    saveError.value = messageNotice("tools.separate.save.failed");
  } finally {
    saveBusy.value = "";
  }
}

onMounted(() => {
  if (caps.slots.length === 0) void caps.load().catch(() => undefined);
  void restoreLastRun();
});

/** 返回分离页时恢复上次已结束的 Run，让「执行过程」可回看。 */
async function restoreLastRun(): Promise<void> {
  const restored = await restoreRun("tool_separate");
  if (!restored) return;
  try {
    resultAssets.value = await loadNodeArtifacts(
      restored.id,
      restored.projectId,
      "03_separate",
    );
  } catch {
    // Run 与执行过程仍可用；产物读不到时不伪造。
  }
}
</script>

<template>
  <WorkbenchPage
    scaffold
    :icon="'scissors'"
    :subtitle="t('tools.separate.intro')"
    :title="t('tools.separate.title')"
    :primary-label="primaryLabel"
    :primary-icon="canOpenProject ? 'folder-open' : 'plus'"
    primary-shortcut
    :primary-disabled="runBusy || (!hasProject && createBlocked)"
    @primary="onPrimary"
  >
    <template #footer-actions>
      <YmButton
        v-if="hasProject && runBusy"
        variant="secondary"
        size="lg"
        icon="folder-open"
        @click="openProject"
      >
        {{ t("common.seeDetails") }}
      </YmButton>
      <YmButton
        v-else-if="hasProject"
        variant="secondary"
        size="lg"
        icon="refresh"
        @click="startNew"
      >
        {{ t("creation.result.again") }}
      </YmButton>
    </template>
    <template #config>
      <div class="page-form">
        <CapabilityAccessNotice access-key="tool-separate" />
        <p v-if="runError && !currentRun" class="flow-error flow-error--form">
          {{ errorText(runError) }}
        </p>
        <ul v-if="runIssues.length && !currentRun" class="flow-issues flow-issues--form">
          <li v-for="issue in runIssues" :key="preflightIssueKey(issue)">
            {{ errorText(issue) }}
          </li>
        </ul>

        <YmPanel :title="t('tools.separate.step.project')">
          <div class="panel-stack">
            <YmField
              :label="t('tools.common.projectName')"
              :hint="t('tools.common.projectNameHint')"
              :error="projectError"
              required
            >
              <YmTextInput
                v-model="projectName"
                :placeholder="t('tools.common.projectName')"
              />
            </YmField>

            <YmField
              :label="t('tools.separate.media.label')"
              :error="mediaError"
              required
            >
              <YmFileRow
                v-if="media"
                :name="media.name"
                :size="formatBytes(media.sizeBytes)"
                :icon="mediaIcon"
                :video="mediaPreviewUrl"
                :media-kind="mediaKind"
              >
                <template #actions>
                  <YmIconButton
                    name="x"
                    :label="t('tools.common.remove')"
                    :size="16"
                    @click="removeMedia"
                  />
                </template>
              </YmFileRow>
              <YmDropzone
                v-else
                :title="t('tools.separate.media.upload')"
                :hint="t('tools.separate.media.hint')"
                accept=".mp4,.mov,.mkv,.m3u8,.mp3,.wav,.aac,.flac,.m4a,video/*,audio/*"
                @files="onMedia($event[0])"
              />
              <p v-if="mediaPathError" class="flow-error">{{ mediaPathError }}</p>
            </YmField>
          </div>
        </YmPanel>

        <CreationNotice
          :items="[
            t('tools.separate.notice.mixed'),
            t('tools.separate.notice.clean'),
          ]"
        />
      </div>
    </template>

    <template #results>
      <div class="results-stack">
        <section class="result-head">
          <div class="result-head__top">
            <h2 class="result-head__title">{{ t("tools.common.result.title") }}</h2>
            <YmBadge v-if="currentRun" :tone="runStatusTone(currentRun.status)">
              {{ t(runStatusLabelKey(currentRun.status)) }}
            </YmBadge>
            <YmBadge v-else-if="runBusy" tone="info">{{ runPhaseLabel }}</YmBadge>
            <YmBadge v-else tone="neutral">{{ t("tools.separate.result.idle") }}</YmBadge>
          </div>
          <p class="result-head__desc">{{ t("tools.separate.result.desc") }}</p>
          <p v-if="runError && currentRun" class="flow-error">{{ errorText(runError) }}</p>
          <p v-if="runPartial" class="flow-warning">{{ t("tools.flow.partial") }}</p>
          <p v-if="resultError" class="flow-error">{{ resultError }}</p>
          <p v-if="saveErrorText" class="flow-error"><bdi>{{ saveErrorText }}</bdi></p>
          <ul v-if="runIssues.length && currentRun" class="flow-issues">
            <li v-for="issue in runIssues" :key="preflightIssueKey(issue)">
              {{ errorText(issue) }}
            </li>
          </ul>
          <div class="cap-block">
            <h3 class="cap-block__title">{{ t("tools.common.capability") }}</h3>
            <div class="cap-list">
              <YmBadge
                v-for="cap in relatedCapabilities"
                :key="cap.slot"
                :tone="slotStateTone(cap.state)"
              >
                {{ t(cap.nameKey) }} · {{ t(slotStateLabelKey(cap.state)) }}
              </YmBadge>
              <span v-if="relatedCapabilities.length === 0" class="cap-hint">
                {{ t("tools.common.capabilityPending") }}
              </span>
            </div>
            <p class="cap-hint">{{ t("tools.common.capabilityHint") }}</p>
          </div>
        </section>

        <YmCard v-if="currentRun || runBusy">
          <template #header>
            <div class="summary-head">
              <span>{{ currentRun?.name ?? t("tools.separate.result.running") }}</span>
              <YmBadge v-if="currentRun" :tone="runStatusTone(currentRun.status)">
                {{ t(runStatusLabelKey(currentRun.status)) }}
              </YmBadge>
              <YmBadge v-else tone="info">{{ runPhaseLabel }}</YmBadge>
            </div>
          </template>
          <ul v-if="runNodeRows.length" class="node-list">
            <li v-for="row in runNodeRows" :key="row.key" class="node-row">
              <span class="node-row__name">{{ row.label }}</span>
              <YmBadge :tone="row.waiting ? 'neutral' : nodeStatusTone(row.status)">
                {{ row.waiting ? t("state.waitingResource") : t(nodeStatusLabelKey(row.status)) }}
              </YmBadge>
              <span class="node-row__progress">{{ row.progressText }}</span>
            </li>
          </ul>
          <p v-else class="run-status">{{ runPhaseLabel }}</p>
        </YmCard>

        <ExecutionLogPanel :task-id="separateTask?.id" :active="runBusy" :task-status="separateTask?.status" />

        <YmCard v-if="resultAssets.length">
          <template #header>
            <div class="summary-head">
              <span>{{ t("tools.separate.result.assets") }}</span>
              <YmBadge tone="success">
                {{ t("tools.separate.result.count", { count: resultAssets.length }) }}
              </YmBadge>
            </div>
          </template>
          <ul class="asset-list">
            <li v-for="asset in resultAssets" :key="asset.id" class="asset-row">
              <YmIcon name="wave" :size="18" class="asset-row__icon" />
              <div class="asset-row__body">
                <span class="asset-row__name" :title="asset.name">{{ asset.name }}</span>
                <span class="asset-row__meta">
                  {{ t("tools.separate.result.audioTrack") }} · {{ formatBytes(asset.sizeBytes) }}
                </span>
              </div>
              <YmButton
                size="sm"
                :disabled="saveBusy === asset.id"
                @click="saveAsset(asset)"
              >
                {{ t("tools.separate.save.action") }}
              </YmButton>
            </li>
          </ul>
          <p v-if="saveMessageText" class="save-message"><bdi>{{ saveMessageText }}</bdi></p>
        </YmCard>

        <div v-if="resultAssets.length" class="notice notice--accent">
          <YmIcon name="info" :size="14" class="notice__icon" />
          <span>{{ t("tools.separate.cloneHint") }}</span>
        </div>

        <div v-else-if="!currentRun && !runBusy" class="results-empty">
          <YmEmpty
            :title="t('tools.common.result.emptyTitle')"
            :description="t('tools.separate.result.emptyDesc')"
            icon="wave"
          />
        </div>
      </div>
    </template>
    <template #footer>{{ footerStatus }}</template>
  </WorkbenchPage>
</template>

<style scoped>
.page-form {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.page-intro {
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  line-height: var(--line-height-normal);
}
.panel-stack {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.notice {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-md);
  font-size: var(--font-size-xs);
  line-height: var(--line-height-normal);
}
.notice--accent {
  background: var(--color-accent-soft);
  color: var(--color-accent-strong);
}
.notice__icon {
  flex: none;
  margin-top: 1px;
}
.results-stack {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.result-head {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-4);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
}
.result-head__top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}
.result-head__title {
  font-size: var(--font-size-lg);
}
.result-head__desc {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.cap-block {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding-top: var(--space-3);
  border-top: 1px solid var(--color-border);
}
.cap-block__title {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.cap-list {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
.cap-hint {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.results-empty {
  background: var(--color-surface);
  border: 1px dashed var(--color-border);
  border-radius: var(--radius-lg);
}
.summary-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}
.flow-error {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-md);
  background: var(--color-error-soft);
  color: var(--color-error-strong);
  font-size: var(--font-size-xs);
  overflow-wrap: anywhere;
}
.flow-warning {
  font-size: var(--font-size-xs);
  color: var(--color-warning-strong);
}
.flow-error--form,
.flow-issues--form {
  margin-bottom: var(--space-3);
}
.flow-issues {
  margin: 0;
  padding-left: var(--space-4);
  color: var(--color-warning-strong);
  font-size: var(--font-size-xs);
}
.node-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  margin: 0;
  padding: 0;
  list-style: none;
}
.node-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-1) 0;
  font-size: var(--font-size-sm);
}
.node-row__name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.node-row__progress {
  flex: none;
  color: var(--color-text-faint);
  font-variant-numeric: tabular-nums;
}
.run-status {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.asset-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  margin: 0;
  padding: 0;
  list-style: none;
}
.asset-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) 0;
  border-bottom: 1px solid var(--color-border);
}
.asset-row:last-child {
  border-bottom: none;
}
.asset-row__icon {
  flex: none;
  color: var(--color-accent-strong);
}
.asset-row__body {
  display: flex;
  flex: 1;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}
.asset-row__name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: var(--font-size-sm);
}
.asset-row__meta {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.save-message {
  margin: var(--space-2) 0 0;
  color: var(--color-success-strong);
  font-size: var(--font-size-xs);
  overflow-wrap: anywhere;
}
</style>
