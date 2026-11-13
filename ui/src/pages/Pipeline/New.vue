<script setup lang="ts">
// 新建全流程译制：业务对齐网页端 ProjectModal（上传视频 + 项目名 + 译制语言 + 擦除方式 + 参数），
// 排版使用客户端 WorkbenchPage 两栏（左配置 / 右结果预览），创建后进入项目工作台。
import { computed, onBeforeUnmount, ref } from "vue";
import { useRouter } from "vue-router";
import { t, formatBytes } from "@/i18n";
import { api, ApiError } from "@/api";
import { TARGET_LANGUAGES } from "@/domain/languages";
import type { EraseMode } from "./types";
import WorkbenchPage from "@/components/layout/WorkbenchPage.vue";
import CreationNotice from "@/components/tools/CreationNotice.vue";
import ConfigStep from "./ConfigStep.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmDropzone from "@/components/ui/YmDropzone.vue";
import YmPanel from "@/components/ui/YmPanel.vue";
import YmFileRow from "@/components/ui/YmFileRow.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmIconButton from "@/components/ui/YmIconButton.vue";
import { useProjectsStore } from "@/stores/projects";

interface SelectedVideo {
  /** 桌面端通过授权文件对话框选择时没有 File；仅浏览器 demo 有。 */
  file: File | null;
  path: string;
  name: string;
  size: number;
}

const router = useRouter();
const projects = useProjectsStore();

const name = ref("");
const targetLanguages = ref<string[]>([]);
const eraseMode = ref<EraseMode>("dialog_only");
const validateTimestamps = ref(false);
const adjustDuration = ref(false);
const bilingual = ref(false);
const videoFile = ref<SelectedVideo | null>(null);
const previewUrl = ref("");
const error = ref("");
const creating = ref(false);
const fileInput = ref<HTMLInputElement | null>(null);

const canCreate = computed(
  () =>
    name.value.trim().length > 0 &&
    Boolean(videoFile.value?.path) &&
    targetLanguages.value.length > 0 &&
    !creating.value,
);
const footerStatus = computed(() => {
  if (creating.value) return t("creation.footer.running");
  const missing: string[] = [];
  if (!name.value.trim()) missing.push(t("pipeline.new.formTitle"));
  if (!videoFile.value?.path) missing.push(t("pipeline.new.sourceTitle"));
  if (targetLanguages.value.length === 0) missing.push(t("pipeline.new.languages"));
  return missing.length
    ? t("creation.footer.missing", { items: missing.join(" · ") })
    : t("creation.footer.ready");
});

function errorText(value: unknown): string {
  if (value instanceof ApiError) return t(value.messageKey);
  return value instanceof Error ? value.message : String(value);
}

function onName(value: string): void {
  name.value = value.slice(0, 30);
}

function toggleTarget(code: string): void {
  const selected = new Set(targetLanguages.value);
  if (selected.has(code)) selected.delete(code);
  else selected.add(code);
  targetLanguages.value = TARGET_LANGUAGES.map((language) => language.code).filter((code) =>
    selected.has(code),
  );
}

function selectAllTargets(): void {
  targetLanguages.value = TARGET_LANGUAGES.map((language) => language.code);
}

function clearTargets(): void {
  targetLanguages.value = [];
}

function setEraseMode(value: string): void {
  if (value === "dialog_only" || value === "dialog_and_art") eraseMode.value = value;
}

async function pickFile(): Promise<void> {
  const bridge = window.yaomu?.api;
  if (bridge?.selectFiles) {
    try {
      const paths = await bridge.selectFiles({
        title: t("pipeline.new.upload"),
        filters: [{ name: t("pipeline.new.upload"), extensions: ["mp4", "mov", "mkv"] }],
      });
      const path = paths[0]?.trim() ?? "";
      if (path) setVideoPath(path);
    } catch (err) {
      error.value = errorText(err);
    }
    return;
  }
  fileInput.value?.click();
}

function setVideoPath(path: string): void {
  const name = path.split(/[\\/]/).pop() || path;
  releasePreview();
  videoFile.value = { file: null, path, name, size: 0 };
  error.value = "";
}

function releasePreview(): void {
  if (previewUrl.value) URL.revokeObjectURL(previewUrl.value);
  previewUrl.value = "";
}

function setVideo(file: File): void {
  const bridge = window.yaomu?.api;
  const bridgePath = bridge?.getPathForFile(file).trim() ?? "";
  // 浏览器 demo 无桌面桥时回退文件名（与工具页一致）；桌面端仍使用真实授权路径。
  const legacy = (file as File & { path?: string }).path?.trim() ?? "";
  const path = bridgePath || legacy || file.name.trim();
  if (!path) {
    error.value = t("pipeline.new.filePathError");
    return;
  }
  releasePreview();
  videoFile.value = { file, path, name: file.name, size: file.size };
  previewUrl.value = URL.createObjectURL(file);
  error.value = "";
}

function onFileChange(event: Event): void {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (file) setVideo(file);
  input.value = "";
}

function onFiles(files: File[]): void {
  if (files[0]) setVideo(files[0]);
}

function removeVideo(): void {
  releasePreview();
  videoFile.value = null;
}

onBeforeUnmount(() => {
  releasePreview();
});

async function create(): Promise<void> {
  if (!name.value.trim()) {
    error.value = t("pipeline.new.nameError");
    return;
  }
  if (!videoFile.value?.path) {
    error.value = t("pipeline.new.videoError");
    return;
  }
  if (targetLanguages.value.length === 0) {
    error.value = t("pipeline.new.languageError");
    return;
  }

  creating.value = true;
  error.value = "";
  let projectId = "";
  try {
    const project = await projects.create(name.value.trim());
    projectId = project.id;
    await api.importAssets({
      projectId: project.id,
      paths: [videoFile.value.path],
      copyMode: false,
    });
    await router.push({
      path: `/projects/${project.id}/pipeline`,
      query: {
        autostart: "1",
        langs: targetLanguages.value.join(","),
        erase: eraseMode.value,
        validate: validateTimestamps.value ? "1" : "0",
        adjust: adjustDuration.value ? "1" : "0",
        bilingual: bilingual.value ? "1" : "0",
      },
    });
  } catch (err) {
    if (projectId) await projects.remove(projectId).catch(() => undefined);
    error.value = t("pipeline.new.importFailed", { error: errorText(err) });
  } finally {
    creating.value = false;
  }
}
</script>

<template>
  <WorkbenchPage
    scaffold
    icon="film"
    :title="t('pipeline.new.title')"
    :subtitle="t('pipeline.new.desc')"
    :primary-label="t('common.create')"
    primary-icon="plus"
    primary-shortcut
    :primary-disabled="!canCreate"
    :primary-loading="creating"
    @primary="create"
  >
    <template #config>
      <div class="page-form">
        <p v-if="error" class="new-error" role="alert">{{ error }}</p>

        <YmPanel :title="t('pipeline.new.sourceTitle')">
          <div class="panel-stack">
          <input
            ref="fileInput"
            class="file-input"
            type="file"
            :aria-label="t('pipeline.new.upload')"
            accept="video/*,.mp4,.mov,.mkv"
            @change="onFileChange"
          />

          <YmDropzone
            v-if="!videoFile"
            :title="t('pipeline.new.upload')"
            :hint="t('pipeline.new.uploadHint')"
            accept="video/*,.mp4,.mov,.mkv"
            @files="onFiles"
          />

          <YmFileRow
            v-else
            :name="videoFile.name"
            :size="videoFile.size > 0 ? formatBytes(videoFile.size) : '—'"
            :video="previewUrl"
            icon="film"
          >
            <template #actions>
              <YmIconButton name="refresh" :label="t('pipeline.new.replace')" @click="pickFile" />
              <YmIconButton name="x" :label="t('pipeline.new.remove')" @click="removeVideo" />
            </template>
          </YmFileRow>

          <p class="hint">{{ t("pipeline.new.oneVideoHint") }}</p>
          </div>
        </YmPanel>

        <CreationNotice
          :items="[
            t('pipeline.new.notice1'),
            t('pipeline.new.notice2'),
            t('pipeline.new.notice3'),
            t('pipeline.new.notice4'),
          ]"
        />

        <YmPanel :title="t('pipeline.new.settingsTitle')">
          <div class="panel-stack">
          <p class="page-intro">{{ t("pipeline.new.settingsDesc") }}</p>
          <ConfigStep
            :project-name="name"
            :target-languages="targetLanguages"
            :target-options="TARGET_LANGUAGES"
            :reuse-audio="false"
            :reuse-subtitle="false"
            :erase-mode="eraseMode"
            :validate-timestamps="validateTimestamps"
            :adjust-duration="adjustDuration"
            :bilingual="bilingual"
            :show-reuse="false"
            @update:project-name="onName"
            @toggle-target="toggleTarget"
            @select-all-targets="selectAllTargets"
            @clear-targets="clearTargets"
            @update:erase-mode="setEraseMode"
            @update:validate-timestamps="validateTimestamps = $event"
            @update:adjust-duration="adjustDuration = $event"
            @update:bilingual="bilingual = $event"
          />
          </div>
        </YmPanel>
      </div>
    </template>

    <template #results>
      <div class="results-stack results-stack--fill">
        <YmCard>
          <template #header>
            <div class="summary-head">
              <span>{{ t("pipeline.new.results") }}</span>
            </div>
          </template>
          <div class="preview">
            <video v-if="previewUrl" class="preview__video" :src="previewUrl" controls></video>
            <div v-else class="preview__empty">
              <span class="preview__icon"><YmIcon name="film" :size="30" /></span>
              <p>{{ t("pipeline.new.resultsPending") }}</p>
            </div>
          </div>
          <p class="hint">{{ t("pipeline.new.ready") }}</p>
        </YmCard>
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
.results-stack {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.results-stack--fill {
  flex: 1;
  min-height: 0;
}
.results-stack--fill :deep(.ym-card) {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-height: 0;
}
.results-stack--fill :deep(.ym-card__body) {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 12px;
  min-height: 0;
}
.results-stack--fill .preview {
  flex: 1;
  min-height: 0;
}
.summary-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}
.new-error {
  margin: 0;
  padding: 10px 12px;
  border-radius: var(--radius-md);
  background: var(--color-error-soft);
  color: var(--color-error-strong);
  font-size: var(--font-size-sm);
}
.file-input {
  position: absolute;
  width: 1px;
  height: 1px;
  opacity: 0;
}
/* 上传区与已选文件行：与 UI 分支的共享上传组件保持同一视觉。 */
.hint {
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
  line-height: 1.5;
}
.preview {
  display: flex;
  flex-direction: column;
  flex: 1;
  overflow: hidden;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-card);
  background: var(--color-surface-2);
}
.preview__video {
  display: block;
  width: 100%;
  height: 100%;
  min-height: 260px;
  object-fit: contain;
  background: var(--color-media);
}
.preview__empty {
  display: flex;
  flex: 1;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  min-height: 240px;
  padding: 28px 20px;
  text-align: center;
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
}
.preview__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 68px;
  height: 68px;
  border-radius: var(--radius-xl);
  border: 1px solid color-mix(in srgb, var(--color-primary) 18%, transparent);
  background: var(--gradient-brand-soft);
  color: var(--color-primary);
}
</style>
