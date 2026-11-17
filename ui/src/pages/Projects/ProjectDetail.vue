<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { api, apiMode } from "@/api";
import { formatBytes, formatDate, formatDuration, t, tCount } from "@/i18n";
import {
  fallbackErrorNotice,
  messageNotice,
  renderNotice,
  type SafeNotice,
} from "@/i18n/notice";
import {
  projectStatusBucket,
  type ProjectStatusBucket,
} from "@/domain/projects";
import { runnerWaitingMessageKey, type Tone } from "@/domain/status";
import { ASR_LANGUAGES, TARGET_LANGUAGES, UI_LANGUAGES } from "@/domain/languages";
import { evaluateAccess } from "@/domain/capabilityAccess";
import type {
  Asset,
  AssetStatus,
  AssetType,
  ExportRecord,
  Language,
  SubtitleTrack,
  Task,
  WorkflowRun,
} from "@/domain/types";
import { buildProjectDownloadItems } from "@/components/projectCardFiles";
import PageHeader from "@/components/layout/PageHeader.vue";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmEmpty from "@/components/ui/YmEmpty.vue";
import YmNotice from "@/components/ui/YmNotice.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmBackButton from "@/components/ui/YmBackButton.vue";
import YmStats, { type StatItem } from "@/components/ui/YmStats.vue";
import { useProjectsStore } from "@/stores/projects";
import { useCapabilitiesStore } from "@/stores/capabilities";
import { useRuntimeStore } from "@/stores/runtime";

const route = useRoute();
const router = useRouter();
const projects = useProjectsStore();
const caps = useCapabilitiesStore();
const runtime = useRuntimeStore();

// 桌面桥存在时“打开项目目录”交给系统文件管理器；浏览器 mock 保留提示。
const desktopApi = typeof window !== "undefined" ? window.yaomu?.api : undefined;

const error = ref<SafeNotice | null>(null);
const notice = ref<SafeNotice | null>(null);
const errorText = computed(() => renderNotice(error.value));
const noticeText = computed(() => renderNotice(notice.value));
let noticeTimer: number | undefined;

const LANGUAGES: Language[] = [...ASR_LANGUAGES, ...TARGET_LANGUAGES, ...UI_LANGUAGES];

const projectId = computed<string>(() => String(route.params.id ?? ""));
const exports = ref<ExportRecord[]>([]);
const project = computed(() =>
  projects.current && projects.current.id === projectId.value ? projects.current : null,
);
const totalDuration = computed<number>(() =>
  (project.value?.assets ?? []).reduce((sum, asset) => sum + (asset.meta?.durationMs ?? 0), 0),
);
const hasTranslation = computed(
  () =>
    (project.value?.runs.length ?? 0) > 0 &&
    (project.value?.tracks ?? []).some((track) => track.type === "translation"),
);

/** 详情页只按 updatedAt 取最新 Run，不在前端另建业务状态。 */
const latestRun = computed<WorkflowRun | undefined>(() => {
  const runs = project.value?.runs ?? [];
  if (!runs.length) return undefined;
  return [...runs].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))[0];
});


const projectTypeKey = computed(() => runTypeKey(latestRun.value));
const projectTypeLabel = computed(() => t(`workbench.type.${projectTypeKey.value}`));

/** 项目状态固定四态，与卡片一致；Run 细节（节点/审核）仍用运行态标签。 */
const STATUS_BUCKET_TONES: Record<ProjectStatusBucket, Tone> = {
  pending: "neutral",
  running: "info",
  completed: "success",
  failed: "error",
};
const statusBucket = computed(() => projectStatusBucket(latestRun.value?.status));

/** 终止的失败/部分成功/取消 Run 无法原地重试：提供「重新创建」回到对应工具。 */
const canRecreate = computed(() =>
  latestRun.value
    ? ["FAILED", "PARTIAL", "CANCELED"].includes(latestRun.value.status)
    : false,
);
const recreateRoute = computed(() => TOOL_ROUTES[runTypeKey(latestRun.value)] ?? "/pipeline/new");
function recreate(): void {
  void router.push(recreateRoute.value);
}

/** 运行中的最新 Run 若任务在等执行器或已中断，提供明确原因与可执行动作。 */
const runTasks = ref<Task[]>([]);
const retryBusy = ref("");
const retryError = ref<SafeNotice | null>(null);
const retryErrorText = computed(() => renderNotice(retryError.value));
const runActive = computed(() => {
  const status = latestRun.value?.status;
  return (
    !!status &&
    ["QUEUED", "RUNNING", "PAUSE_REQUESTED", "PAUSED", "WAITING_REVIEW"].includes(status)
  );
});
const waitingTask = computed(() =>
  runTasks.value.find((task) => task.status === "WAITING_RESOURCE"),
);
const retryableTask = computed(() =>
  runTasks.value.find(
    (task) =>
      (task.status === "FAILED" || task.status === "INTERRUPTED" || task.status === "PAUSED") &&
      task.error?.retryable,
  ),
);

async function loadRunTasks(runId: string | undefined): Promise<void> {
  if (!runId) {
    runTasks.value = [];
    return;
  }
  try {
    const page = await api.listTasks({ runId, limit: 200 });
    if (latestRun.value?.id !== runId) return;
    runTasks.value = page.items;
  } catch {
    // 任务读回失败不改变项目事实；下一次刷新或事件会补上。
  }
}

watch(
  [() => latestRun.value?.id, () => project.value?.updatedAt],
  () => {
    void loadRunTasks(latestRun.value?.id);
  },
  { immediate: true },
);

async function retryTask(task: Task): Promise<void> {
  retryBusy.value = task.id;
  retryError.value = null;
  try {
    await api.retryTask(task.id);
    await loadProject(projectId.value);
    await loadRunTasks(latestRun.value?.id);
  } catch (value) {
    retryError.value = fallbackErrorNotice(value, "projectDetail.loadError");
  } finally {
    retryBusy.value = "";
  }
}

function openModels(): void {
  void router.push("/settings/models");
}
const statusTone = computed(() => STATUS_BUCKET_TONES[statusBucket.value]);
const statusLabel = computed(() => t(`projects.filter.status.${statusBucket.value}`));
const hasKnownType = computed(() => projectTypeKey.value !== "project");

const projectStats = computed<StatItem[]>(() => {
  const current = project.value;
  const items: StatItem[] = [
    { value: current?.assets.length ?? 0, label: t("projectDetail.summary.assets") },
    { value: current?.tracks.length ?? 0, label: t("projectDetail.summary.tracks") },
  ];
  // 无 Run 时不展示恒为 0 的运行统计。
  if (current?.runs.length) {
    items.push({ value: current.runs.length, label: t("projectDetail.summary.runs") });
  }
  items.push({
    value: formatDuration(totalDuration.value),
    label: t("projectDetail.summary.duration"),
    dir: "ltr",
  });
  return items;
});

/** 复用卡片下载的命名/去重逻辑，详情页不另写建议文件名规则。 */
const downloadNames = computed<Map<string, string>>(() => {
  const current = project.value;
  if (!current) return new Map();
  return new Map(
    buildProjectDownloadItems({
      projectName: current.name,
      assets: current.assets,
    }).map((item) => [item.asset.id, item.filename]),
  );
});

onMounted(() => {
  void loadProject(projectId.value);
});

watch(projectId, (id) => {
  void loadProject(id);
});

// 宿主重连：只刷新当前项目数据；重命名对话框的本地文本不重挂、不被覆盖。
let refreshedEpoch = runtime.readyEpoch;
watch(
  () => runtime.readyEpoch,
  (epoch) => {
    if (!runtime.gated || !epoch || epoch === refreshedEpoch) return;
    refreshedEpoch = epoch;
    const id = projectId.value;
    if (!id) return;
    error.value = null;
    void projects.open(id).catch((value) => {
      error.value = fallbackErrorNotice(value, "projectDetail.loadError");
    });
  },
);

onBeforeUnmount(() => {
  if (noticeTimer !== undefined) window.clearTimeout(noticeTimer);
});

async function loadProject(id: string): Promise<void> {
  if (!id) return;
  error.value = null;
  if (projects.current?.id === id) return;
  try {
    await projects.open(id);
    await loadExports(id);
  } catch (value) {
    error.value = fallbackErrorNotice(value, "projectDetail.loadError");
  }
}

function goBack(): void {
  void router.push("/projects");
}

function openCorrection(): void {
  void router.push(`/projects/${projectId.value}/correction`);
}

/**
 * 无 Run 项目从详情继续：工具项目回到对应工具页（Run 由工具页创建），
 * 全流程/未知类型才进入流水线向导；准入隐藏时保留项目上下文回到工作台。
 */
const TOOL_ROUTES: Record<string, string> = {
  separation: "/tools/separate",
  erase: "/tools/erase",
  translate: "/tools/translate",
  extract: "/tools/extract",
  custom: "/tools/custom",
};

async function continueProject(): Promise<void> {
  const current = project.value;
  if (!current) return;
  const toolRoute = TOOL_ROUTES[runTypeKey(latestRun.value)];
  if (toolRoute) {
    await router.push(toolRoute);
    return;
  }
  if (caps.slots.length === 0) {
    try {
      await caps.load();
    } catch {
      // 能力目录不可用时保留入口：Core 预检仍是最终权威。
    }
  }
  if (evaluateAccess(caps.slots, "pipeline").level === "hidden") {
    await router.push({ path: "/", query: { project: current.id } });
    return;
  }
  try {
    await projects.open(current.id);
    await router.push(`/projects/${current.id}/pipeline`);
  } catch (value) {
    error.value = fallbackErrorNotice(value, "projectDetail.loadError");
  }
}

function flashNotice(): void {
  if (noticeTimer !== undefined) window.clearTimeout(noticeTimer);
  noticeTimer = window.setTimeout(() => {
    notice.value = null;
  }, 5000);
}

async function loadExports(id: string): Promise<void> {
  try {
    exports.value = await api.listExports(id);
  } catch {
    exports.value = [];
  }
}

function containingDirectory(filePath: string): string {
  const normalized = filePath.replace(/\\/g, "/");
  const slash = normalized.lastIndexOf("/");
  return slash > 0 ? filePath.slice(0, slash) : filePath;
}

/** 只打开用户可见的最近导出目录；无导出时按钮保持禁用。 */
const exportFolder = computed(() => {
  const completed = exports.value
    .filter((record) => record.status === "SUCCEEDED" && record.outputPath)
    .sort((left, right) => right.createdAt.localeCompare(left.createdAt));
  const outputPath = completed[0]?.outputPath;
  return outputPath ? containingDirectory(outputPath) : "";
});

async function openFolder(): Promise<void> {
  const current = project.value;
  if (!current || !exportFolder.value) return;
  if (!desktopApi) {
    notice.value = messageNotice("projectDetail.folder.mock", { name: current.name });
    flashNotice();
    return;
  }
  error.value = null;
  try {
    await desktopApi.openPath(exportFolder.value);
  } catch {
    error.value = messageNotice("projectDetail.folder.openFailed");
  }
}

async function downloadAsset(asset: Asset): Promise<void> {
  const name = downloadNames.value.get(asset.id);
  if (!name) return;
  if (apiMode === "unavailable") {
    notice.value = messageNotice("workbench.card.downloadDesktop");
    flashNotice();
    return;
  }
  error.value = null;
  try {
    // 与项目卡片共用 api.saveAsset：桌面桥取消返回 null，失败走安全提示。
    const saved = await api.saveAsset({ assetId: asset.id, suggestedName: name });
    if (saved) {
      notice.value = messageNotice("workbench.card.downloaded", { name });
      flashNotice();
    }
  } catch {
    notice.value = messageNotice("workbench.card.downloadFailed");
    flashNotice();
  }
}

/** 全量 Project 不带 modeType：有 Run 用 Run 的类型，否则只使用已加载的列表摘要。 */
function typeKeyFromMode(modeType?: string): string {
  if (!modeType) return "project";
  if (modeType.startsWith("full_pipeline")) return "pipeline";
  if (modeType === "custom_workflow") return "custom";
  if (modeType.startsWith("audio_separate") && !modeType.includes("_compose")) {
    return "separation";
  }
  if (modeType.startsWith("subtitle_erase_dialog")) {
    return modeType.includes("_compose") ? "custom" : "erase";
  }
  if (modeType.startsWith("subtitle_translate")) {
    return modeType.includes("_compose") ? "custom" : "translate";
  }
  if (modeType.startsWith("high_sentiment_tts_clone") || modeType.startsWith("tts_clone")) {
    return modeType.includes("_compose") ? "custom" : "clone";
  }
  if (modeType.startsWith("subtitle_extract_")) {
    return modeType.includes("_compose") ? "custom" : "extract";
  }
  return "custom";
}

function runTypeKey(run?: WorkflowRun): string {
  const modeType =
    run?.modeType ?? projects.items.find((item) => item.id === projectId.value)?.modeType;
  return typeKeyFromMode(modeType);
}

function langName(code: string): string {
  const language = LANGUAGES.find((item) => item.code === code);
  return language?.nativeName ?? language?.name ?? code;
}

function segmentCount(track: SubtitleTrack): number {
  const revision =
    track.revisions.find((item) => item.id === track.currentRevisionId) ?? track.revisions.at(-1);
  return revision?.segments.length ?? 0;
}

function assetIcon(type: AssetType): string {
  switch (type) {
    case "video":
      return "film";
    case "audio":
    case "audio_track":
      return "volume";
    case "subtitle":
      return "subtitles";
    case "image":
      return "box";
    case "archive":
      return "box";
    default:
      return "box";
  }
}

function assetStatusTone(status: AssetStatus): Tone {
  switch (status) {
    case "ready":
      return "success";
    case "importing":
      return "info";
    case "missing":
    case "corrupt":
      return "error";
  }
}

</script>

<template>
  <div class="page">
    <YmNotice v-if="noticeText" tone="success"><bdi>{{ noticeText }}</bdi></YmNotice>
    <YmNotice v-if="errorText && project" tone="error"><bdi>{{ errorText }}</bdi></YmNotice>

    <!-- 加载态保持基线可返回：项目数据到达前没有共享页头承载返回按钮。 -->
    <div v-if="!project && projects.loading" class="loading-back">
      <YmBackButton
        :label="t('projectDetail.back')"
        :text="t('projectDetail.back')"
        @click="goBack"
      />
    </div>

    <template v-if="project">
      <PageHeader
        :title="project.name"
        icon="folder"
      >
        <template #description>
          <bdi>{{ t("projectDetail.created", { date: formatDate(project.createdAt) }) }}</bdi> ·
          <bdi>{{ t("projectDetail.updated", { date: formatDate(project.updatedAt) }) }}</bdi>
        </template>
        <template #meta>
          <YmBadge v-if="hasKnownType" tone="info">{{ projectTypeLabel }}</YmBadge>
          <YmBadge solid :tone="statusTone">{{ statusLabel }}</YmBadge>
        </template>
        <template #leading>
          <YmBackButton
            :label="t('projectDetail.back')"
            :text="t('projectDetail.back')"
            @click="goBack"
          />
        </template>
        <template #actions>
          <YmButton
            v-if="canRecreate"
            icon="refresh"
            data-testid="project-detail-recreate"
            @click="recreate"
          >
            {{ t("projectDetail.recreate") }}
          </YmButton>
          <YmButton
            v-if="hasTranslation"
            icon="subtitles"
            data-testid="project-detail-correction"
            @click="openCorrection"
          >
            {{ t("projects.correct") }}
          </YmButton>
          <YmButton
            icon="folder-open"
            :disabled="!exportFolder"
            :title="exportFolder ? t('pipeline.export.openFolder') : t('pipeline.export.openFolderPending')"
            @click="openFolder"
          >
            {{ t("pipeline.export.openFolder") }}
          </YmButton>
        </template>
      </PageHeader>

      <YmStats variant="card" :items="projectStats" />

      <YmNotice
        v-if="runActive && waitingTask"
        tone="warning"
        :title="t('state.waitingResource')"
      >
        {{ t(runnerWaitingMessageKey(waitingTask?.error?.messageKey)) }}
        <template #actions>
          <YmButton size="sm" variant="secondary" icon="gear" @click="openModels">
            {{ t("access.viewModels") }}
          </YmButton>
        </template>
      </YmNotice>

      <YmNotice
        v-else-if="runActive && retryableTask"
        tone="error"
        :title="t('state.failed')"
      >
        <p v-if="retryableTask.error" class="muted">
          {{ t(retryableTask.error.messageKey) }}
        </p>
        <p v-if="retryErrorText" role="alert"><bdi>{{ retryErrorText }}</bdi></p>
        <template #actions>
          <YmButton
            size="sm"
            variant="primary"
            icon="refresh"
            :loading="retryBusy === retryableTask.id"
            :disabled="retryBusy === retryableTask.id"
            @click="retryTask(retryableTask)"
          >
            {{ t("common.retry") }}
          </YmButton>
        </template>
      </YmNotice>


      <YmCard v-if="!latestRun" data-testid="project-detail-not-started">
        <div class="project-not-started">
          <h2>{{ t("projectDetail.notStartedTitle") }}</h2>
          <p>{{ t("projectDetail.notStartedDesc") }}</p>
          <!-- 项目作用域流水线入口；不引入 autostart。 -->
          <YmButton
            variant="primary"
            icon="plus"
            data-testid="project-detail-create-task"
            @click="continueProject"
          >
            {{ t("projectDetail.createTask") }}
          </YmButton>
        </div>
      </YmCard>

      <YmCard>
        <template #header>
          <div class="card-head">
            <h2 class="card-title">{{ t("projectDetail.assets.title") }}</h2>
            <span class="muted">
              <bdi>{{ tCount("projectDetail.assets.count", project.assets.length) }}</bdi>
            </span>
          </div>
        </template>
        <YmEmpty
          v-if="!project.assets.length"
          icon="film"
          :title="t('projectDetail.assets.empty')"
        />
        <ul v-else class="row-list">
          <li v-for="asset in project.assets" :key="asset.id" class="asset-row">
            <span class="row-icon"><YmIcon :name="assetIcon(asset.type)" :size="18" /></span>
            <div class="asset-row__main">
              <span class="asset-row__name truncate"><bdi dir="auto">{{ asset.name }}</bdi></span>
              <span class="muted">{{ t(`projectDetail.assetType.${asset.type}`) }}</span>
            </div>
            <YmBadge :tone="assetStatusTone(asset.status)">
              {{ t(`projectDetail.assetStatus.${asset.status}`) }}
            </YmBadge>
            <span class="asset-row__meta mono">
              <bdi dir="ltr">{{ asset.meta ? formatDuration(asset.meta.durationMs) : "—" }}</bdi>
            </span>
            <span class="asset-row__meta mono"><bdi dir="ltr">{{ formatBytes(asset.sizeBytes) }}</bdi></span>
            <YmButton
              v-if="downloadNames.has(asset.id)"
              size="sm"
              variant="ghost"
              icon="download"
              :data-testid="`project-detail-download-${asset.id}`"
              @click="downloadAsset(asset)"
            >
              {{ t("workbench.card.download") }}
            </YmButton>
          </li>
        </ul>
      </YmCard>

      <YmCard>
        <template #header>
          <div class="card-head">
            <h2 class="card-title">{{ t("projectDetail.tracks.title") }}</h2>
            <span class="muted">{{ project.tracks.length }}</span>
          </div>
        </template>
        <YmEmpty
          v-if="!project.tracks.length"
          icon="subtitles"
          :title="t('projectDetail.tracks.empty')"
        />
        <ul v-else class="row-list">
          <li v-for="track in project.tracks" :key="track.id" class="track-row">
            <span class="row-icon"><YmIcon name="subtitles" :size="18" /></span>
            <div class="track-row__main">
              <span class="track-row__name">{{ langName(track.language) }}</span>
              <span class="muted">{{ t(`projectDetail.trackType.${track.type}`) }}</span>
            </div>
            <span class="muted">
              <bdi>{{ tCount("projectDetail.tracks.segments", segmentCount(track)) }}</bdi>
            </span>
            <YmBadge tone="neutral">
              <bdi>{{ tCount("projectDetail.tracks.revisions", track.revisions.length) }}</bdi>
            </YmBadge>
          </li>
        </ul>
      </YmCard>


    </template>

    <div v-else-if="projects.loading" class="loading">{{ t("projectDetail.loading") }}</div>

    <YmEmpty
      v-else
      fill
      icon="folder"
      :title="errorText ? t('projectDetail.loadError') : t('projectDetail.notFound')"
      :description="errorText || t('projectDetail.notFoundDesc')"
    >
      <template #action>
        <YmButton @click="goBack">{{ t("projectDetail.back") }}</YmButton>
      </template>
    </YmEmpty>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 20px;
  max-width: 1240px;
  min-height: 100%;
  margin: 0 auto;
  padding: 24px var(--page-gutter) var(--page-bottom);
}
.loading-back {
  align-self: flex-start;
}
.project-not-started {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--space-2);
}
.project-not-started h2 {
  margin: 0;
  font-size: var(--font-size-lg);
}
.project-not-started p {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}
.card-title {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--font-size-md);
  font-weight: 600;
}
.card-title::before {
  content: "";
  width: 4px;
  height: 14px;
  flex: none;
  border-radius: var(--radius-pill);
  background: var(--gradient-primary);
}
.muted {
  color: var(--color-text-faint);
  font-size: var(--font-size-sm);
}
.row-list {
  display: flex;
  flex-direction: column;
  margin: 0;
  padding: 0;
  list-style: none;
}
.asset-row,
.track-row,
.task-row {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-2) 0;
  border-bottom: 1px solid var(--color-border);
}
.asset-row:last-child,
.track-row:last-child,
.task-row:last-child {
  border-bottom: none;
}
.row-icon {
  display: inline-flex;
  flex: none;
  color: var(--color-text-muted);
}
.asset-row__main,
.track-row__main,
.task-row__main {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.asset-row__name,
.track-row__name,
.task-row__name {
  font-weight: 500;
}
.asset-row__meta {
  flex: none;
  min-width: 64px;
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  text-align: end;
}
.task-row__bar {
  flex: 0 1 120px;
}
.loading {
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.notice {
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.notice--error {
  background: var(--color-error-soft);
  color: var(--color-error-strong);
}
.rename-error {
  margin-top: var(--space-2);
  color: var(--color-error-strong);
  font-size: var(--font-size-sm);
}
@media (max-width: 640px) {
      .asset-row,
  .track-row,
  .task-row {
    flex-wrap: wrap;
  }
  .asset-row__main,
  .track-row__main,
  .task-row__main {
    flex-basis: calc(100% - 48px);
  }
  .asset-row__meta {
    min-width: 0;
    text-align: start;
  }
}
</style>
