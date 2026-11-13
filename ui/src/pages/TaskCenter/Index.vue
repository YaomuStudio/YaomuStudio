<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { formatDate, formatPercent, t } from "@/i18n";
import { diagnosticErrorCode, errorText } from "@/i18n/errorDisplay";
import {
  countNotice,
  fallbackErrorNotice,
  messageNotice,
  noticeList,
  renderNotice,
  type SafeNotice,
} from "@/i18n/notice";
import { taskStatusLabelKey, taskStatusTone, type Tone } from "@/domain/status";
import { isTaskProgressIndeterminate } from "@/domain/taskProgress";
import type { PipelineNodeKey, Task, TaskStatus } from "@/domain/types";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmEmpty from "@/components/ui/YmEmpty.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmNotice from "@/components/ui/YmNotice.vue";
import YmProgress from "@/components/ui/YmProgress.vue";
import YmSkeleton from "@/components/ui/YmSkeleton.vue";
import { useProjectsStore } from "@/stores/projects";
import { useTasksStore } from "@/stores/tasks";

const router = useRouter();
const tasks = useTasksStore();
const projects = useProjectsStore();

const busyId = ref<string | null>(null);
const actionError = ref<SafeNotice | null>(null);
const loadError = ref<SafeNotice | null>(null);

const RANK: Record<TaskStatus, number> = {
  RUNNING: 0,
  QUEUED: 1,
  WAITING_RESOURCE: 2,
  PAUSE_REQUESTED: 3,
  PAUSED: 4,
  CANCEL_REQUESTED: 5,
  FAILED: 6,
  INTERRUPTED: 7,
  SUCCEEDED: 8,
  CANCELED: 9,
};

interface Counts {
  queued: number;
  running: number;
  paused: number;
  canceled: number;
  failed: number;
  succeeded: number;
  pendingReview: number;
  partial: number;
}

type FilterKey = "all" | "queued" | "running" | "paused" | "canceled" | "failed" | "succeeded";

interface OverviewItem {
  key: FilterKey;
  label: string;
  tone: Tone;
  count: number;
}

const sortedTasks = computed<Task[]>(() =>
  [...tasks.items].sort(
    (a, b) => RANK[a.status] - RANK[b.status] || b.updatedAt.localeCompare(a.updatedAt),
  ),
);

const projectNames = computed<Record<string, string>>(() =>
  Object.fromEntries(projects.items.map((project) => [project.id, project.name])),
);

const counts = computed<Counts>(() => {
  const next: Counts = {
    queued: 0,
    running: 0,
    paused: 0,
    canceled: 0,
    failed: 0,
    succeeded: 0,
    pendingReview: 0,
    partial: 0,
  };
  for (const task of tasks.items) {
    switch (task.status) {
      case "QUEUED":
      case "WAITING_RESOURCE":
        next.queued += 1;
        break;
      case "RUNNING":
        next.running += 1;
        break;
      case "PAUSE_REQUESTED":
      case "PAUSED":
      case "CANCEL_REQUESTED":
        next.paused += 1;
        break;
      case "CANCELED":
        next.canceled += 1;
        break;
      case "FAILED":
      case "INTERRUPTED":
        next.failed += 1;
        break;
      case "SUCCEEDED":
        next.succeeded += 1;
        break;
      default:
        break;
    }
  }
  for (const project of projects.items) {
    if (project.latestRunStatus === "WAITING_REVIEW") next.pendingReview += 1;
    if (project.latestRunStatus === "PARTIAL") next.partial += 1;
  }
  return next;
});

const overview = computed<OverviewItem[]>(() => [
  {
    key: "all",
    label: t("tasks.filter.all"),
    tone: "info",
    count: tasks.items.length,
  },
  {
    key: "queued",
    label: t(taskStatusLabelKey("QUEUED")),
    tone: taskStatusTone("QUEUED"),
    count: counts.value.queued,
  },
  {
    key: "running",
    label: t(taskStatusLabelKey("RUNNING")),
    tone: taskStatusTone("RUNNING"),
    count: counts.value.running,
  },
  {
    key: "paused",
    label: t(taskStatusLabelKey("PAUSED")),
    tone: taskStatusTone("PAUSED"),
    count: counts.value.paused,
  },
  {
    key: "canceled",
    label: t(taskStatusLabelKey("CANCELED")),
    tone: taskStatusTone("CANCELED"),
    count: counts.value.canceled,
  },
  {
    key: "failed",
    label: t(taskStatusLabelKey("FAILED")),
    tone: taskStatusTone("FAILED"),
    count: counts.value.failed,
  },
  {
    key: "succeeded",
    label: t("tasks.stat.succeeded"),
    tone: taskStatusTone("SUCCEEDED"),
    count: counts.value.succeeded,
  },
]);

const filter = ref<FilterKey>("all");
const keyword = ref("");

const loadErrorText = computed(() => renderNotice(loadError.value));
const actionErrorText = computed(() => renderNotice(actionError.value));

/**
 * 需关注提示按两个计数族分别复数后组合；0 的片段整段省略，
 * 不用 "(s)" 或拼接英文后缀。前缀与分隔符都是已注册文案。
 */
const attentionNotice = computed<SafeNotice | null>(() => {
  const parts: SafeNotice[] = [];
  if (counts.value.pendingReview > 0) {
    parts.push(countNotice("tasks.attention.review", counts.value.pendingReview));
  }
  if (counts.value.partial > 0) {
    parts.push(countNotice("tasks.attention.partial", counts.value.partial));
  }
  if (parts.length === 0) return null;
  return noticeList(
    [
      messageNotice("tasks.attention.prefix"),
      noticeList(parts, t("tasks.attention.separator")),
    ],
    t("tasks.attention.join"),
  );
});
const attentionText = computed(() => renderNotice(attentionNotice.value));

const FILTER_STATUSES: Record<Exclude<FilterKey, "all">, TaskStatus[]> = {
  queued: ["QUEUED", "WAITING_RESOURCE"],
  running: ["RUNNING"],
  paused: ["PAUSE_REQUESTED", "PAUSED", "CANCEL_REQUESTED"],
  canceled: ["CANCELED"],
  failed: ["FAILED", "INTERRUPTED"],
  succeeded: ["SUCCEEDED"],
};

/** 统计卡可点击筛选；筛选与搜索叠加。 */
const visibleTasks = computed(() => {
  const statuses = filter.value === "all" ? null : FILTER_STATUSES[filter.value];
  const kw = keyword.value.trim().toLowerCase();
  return sortedTasks.value.filter((task) => {
    if (statuses && !statuses.includes(task.status)) return false;
    if (kw) {
      const haystack = `${task.label} ${projectLabel(task.projectId)}`.toLowerCase();
      if (!haystack.includes(kw)) return false;
    }
    return true;
  });
});

async function refresh(): Promise<void> {
  loadError.value = null;
  try {
    await Promise.all([tasks.load(), projects.load()]);
  } catch {
    loadError.value = messageNotice("tasks.loadError");
  }
}

onMounted(() => {
  void refresh();
});

function projectLabel(projectId: string): string {
  return projectNames.value[projectId] ?? projectId;
}

function nodeLabel(key: PipelineNodeKey): string {
  return t(`tasks.node.${key}`);
}

function canRetry(task: Task): boolean {
  return task.status === "FAILED" && (task.error?.retryable ?? true);
}

function canCancel(task: Task): boolean {
  return (
    task.status === "QUEUED" ||
    task.status === "WAITING_RESOURCE" ||
    task.status === "RUNNING" ||
    task.status === "PAUSE_REQUESTED" ||
    task.status === "PAUSED"
  );
}

function showsProgress(task: Task): boolean {
  return task.status === "RUNNING" || task.status === "PAUSE_REQUESTED";
}

/** 未知百分比显示诚实等待态；不把缺失/非法进度写成 0%。 */
function progressText(task: Task): string {
  return isTaskProgressIndeterminate(task) || typeof task.progress !== "number"
    ? t("tasks.progress.waiting")
    : formatPercent(task.progress);
}
async function runAction(id: string, action: () => Promise<void>): Promise<void> {
  if (busyId.value) return;
  busyId.value = id;
  actionError.value = null;
  try {
    await action();
  } catch (value) {
    actionError.value = fallbackErrorNotice(value, "tasks.actionFailed");
  } finally {
    busyId.value = null;
  }
}

async function retryTask(task: Task): Promise<void> {
  await runAction(task.id, () => tasks.retry(task.id));
}

async function cancelTask(task: Task): Promise<void> {
  await runAction(task.id, () => tasks.cancel(task.id));
}

async function goProject(projectId: string): Promise<void> {
  await router.push(`/projects/${projectId}`);
}
</script>

<template>
  <div class="page">
    <header class="page__head">
      <div>
        <h1 class="page__title">{{ t("tasks.title") }}</h1>
        <p class="page__subtitle">{{ t("tasks.subtitle") }}</p>
      </div>
    </header>

    <YmNotice v-if="loadErrorText" tone="error"><bdi>{{ loadErrorText }}</bdi></YmNotice>
    <YmNotice v-if="actionErrorText" tone="error"><bdi>{{ actionErrorText }}</bdi></YmNotice>

    <template v-if="tasks.loading && !tasks.items.length">
      <p class="visually-hidden" role="status">{{ t("tasks.loading") }}</p>
      <div class="task-skeleton" aria-hidden="true">
        <YmCard>
          <template #header>
            <YmSkeleton width="120px" height="16px" />
          </template>
          <div class="overview">
            <span v-for="n in 7" :key="n" class="overview__item overview__item--skeleton">
              <YmSkeleton width="64px" height="20px" radius="999px" />
              <YmSkeleton width="18px" height="14px" />
            </span>
          </div>
        </YmCard>

        <div class="toolbar">
          <span class="toolbar__search">
            <YmSkeleton width="100%" height="34px" />
          </span>
          <YmSkeleton width="76px" height="26px" radius="8px" />
        </div>

        <ul class="task-list">
          <li v-for="n in 3" :key="n" class="task-card task-card--skeleton">
            <div class="task-card__head">
              <div class="task-card__title">
                <YmSkeleton width="180px" height="16px" />
                <YmSkeleton width="52px" height="20px" radius="999px" />
              </div>
              <YmSkeleton width="56px" height="26px" radius="8px" />
            </div>
            <div class="task-card__meta">
              <YmSkeleton width="220px" height="11px" />
            </div>
            <div class="task-card__progress">
              <span class="task-card__bar">
                <YmSkeleton width="100%" height="6px" radius="999px" />
              </span>
              <YmSkeleton width="36px" height="11px" />
            </div>
            <div class="task-card__foot">
              <YmSkeleton width="72px" height="14px" />
            </div>
          </li>
        </ul>
      </div>
    </template>

    <YmEmpty
      v-else-if="!sortedTasks.length"
      fill
      icon="list"
      :title="t('tasks.empty.title')"
      :description="t('tasks.empty.desc')"
    >
      <template #action>
        <YmButton variant="primary" size="sm" icon="home" @click="router.push('/')">
          {{ t("tasks.empty.action") }}
        </YmButton>
      </template>
    </YmEmpty>

    <template v-else>
      <YmCard>
        <template #header>
          <h2 class="card-title">{{ t("tasks.summary.title") }}</h2>
        </template>
        <div class="overview" role="tablist" :aria-label="t('tasks.summary.title')">
          <button
            v-for="item in overview"
            :key="item.key"
            type="button"
            role="tab"
            class="overview__item"
            :class="{ 'is-active': filter === item.key }"
            :aria-selected="filter === item.key"
            @click="filter = item.key"
          >
            <YmBadge :tone="item.tone">{{ item.label }}</YmBadge>
            <span class="overview__count">{{ item.count }}</span>
          </button>
        </div>
      </YmCard>

      <p v-if="counts.pendingReview || counts.partial" class="attention" role="status">
        <YmIcon name="alert" :size="14" />
        <bdi>{{ attentionText }}</bdi>
        <button type="button" class="link" @click="router.push('/projects')">
          {{ t("tasks.attention.go") }}
        </button>
      </p>

      <div class="toolbar">
        <label class="toolbar__search">
          <YmIcon name="search" :size="14" />
          <input
            v-model="keyword"
            type="search"
            class="toolbar__input"
            :placeholder="t('tasks.search')"
          />
        </label>
        <YmButton size="sm" icon="refresh" :loading="tasks.loading" @click="refresh">
          {{ t("tasks.refresh") }}
        </YmButton>
      </div>

      <p v-if="!visibleTasks.length" class="filter-empty">{{ t("tasks.filter.empty") }}</p>

      <ul v-else class="task-list">
        <li
          v-for="task in visibleTasks"
          :key="task.id"
          class="task-card"
          :class="{ 'ym-ambient': task.status === 'RUNNING', 'is-running': task.status === 'RUNNING' }"
        >
          <div class="task-card__head">
            <div class="task-card__title">
              <span class="truncate">{{ task.label }}</span>
              <YmBadge :tone="taskStatusTone(task.status)">
                {{ t(taskStatusLabelKey(task.status)) }}
              </YmBadge>
            </div>
            <div class="task-card__actions">
              <YmButton
                v-if="canRetry(task)"
                size="sm"
                icon="refresh"
                :loading="busyId === task.id"
                @click="retryTask(task)"
              >
                {{ t("common.retry") }}
              </YmButton>
              <YmButton
                v-if="canCancel(task)"
                variant="ghost"
                size="sm"
                icon="x"
                :loading="busyId === task.id"
                @click="cancelTask(task)"
              >
                {{ t("common.cancel") }}
              </YmButton>
            </div>
          </div>

          <div class="task-card__meta">
            <span v-if="task.nodeKey">{{ nodeLabel(task.nodeKey) }}</span>
            <span>{{ t("tasks.project", { name: projectLabel(task.projectId) }) }}</span>
            <span>{{ t("tasks.updated", { date: formatDate(task.updatedAt) }) }}</span>
          </div>

          <div v-if="showsProgress(task)" class="task-card__progress">
            <YmProgress
              class="task-card__bar"
              :value="task.progress"
              :indeterminate="isTaskProgressIndeterminate(task)"
              :tone="taskStatusTone(task.status)"
            />
            <span class="task-card__percent mono">{{ progressText(task) }}</span>
          </div>

          <div v-if="task.error" class="task-card__error">
            <YmIcon name="alert" :size="14" />
            <span>{{ errorText(task.error) }}</span>
            <span
              v-if="diagnosticErrorCode(task.error)"
              class="task-card__error-code mono"
            >
              {{ diagnosticErrorCode(task.error) }}
            </span>
          </div>

          <div class="task-card__foot">
            <button class="link" type="button" @click="goProject(task.projectId)">
              {{ t("tasks.viewProject") }}
            </button>
          </div>
        </li>
      </ul>
    </template>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  max-width: 1000px;
  min-height: 100%;
  margin: 0 auto;
  padding: var(--space-6) var(--space-6) var(--page-bottom);
}
.page__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-4);
}
.page__title {
  font-size: var(--font-size-xl);
}
.page__subtitle {
  margin-top: var(--space-1);
  color: var(--color-text-muted);
  font-size: var(--font-size-md);
}
.card-title {
  font-size: var(--font-size-md);
}
.overview {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
.overview__item {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  background: var(--color-surface-2);
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: background var(--motion-duration-snap) var(--motion-ease-snap), border-color var(--motion-duration-snap) var(--motion-ease-snap);
}
.overview__item:hover {
  background: var(--color-surface-3);
}
.overview__item.is-active {
  border-color: var(--color-primary);
  background: var(--color-primary-soft);
}
.overview__item--skeleton {
  cursor: default;
  pointer-events: none;
}
.overview__count {
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.task-skeleton {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.attention {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-warning-soft);
  border-radius: var(--radius-md);
  background: var(--color-warning-soft);
  color: var(--color-warning-strong);
  font-size: var(--font-size-sm);
}
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
}
.toolbar__search {
  display: flex;
  flex: 1;
  align-items: center;
  gap: var(--space-2);
  max-width: 360px;
  padding: 0 var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  color: var(--color-text-faint);
}
.toolbar__search:focus-within {
  border-color: var(--color-primary);
  box-shadow: var(--focus-ring);
}
.toolbar__input {
  flex: 1;
  min-width: 0;
  height: 34px;
  border: 0;
  background: transparent;
  color: var(--color-text);
  font-size: var(--font-size-sm);
  outline: none;
}
.toolbar__input:focus-visible {
  /* 焦点环由外层 :focus-within 统一绘制，避免内外两层同心边框。 */
  box-shadow: none;
}
.filter-empty {
  padding: var(--space-6);
  color: var(--color-text-faint);
  font-size: var(--font-size-sm);
  text-align: center;
}
.task-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  margin: 0;
  padding: 0;
  list-style: none;
}
.task-card {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-4);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  transition:
    border-color var(--motion-duration-base) var(--motion-ease-out),
    box-shadow var(--motion-duration-base) var(--motion-ease-out);
}
.task-card:hover {
  box-shadow: var(--shadow-sm);
}
.task-card.is-running {
  border-color: color-mix(in srgb, var(--color-primary) 30%, var(--color-border));
}
.task-card--skeleton {
  pointer-events: none;
}
.task-card__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-3);
}
.task-card__title {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
  font-weight: 500;
}
.task-card__actions {
  display: flex;
  flex: none;
  align-items: center;
  gap: var(--space-2);
}
.task-card__meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-1) var(--space-3);
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.task-card__progress {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.task-card__bar {
  flex: 1;
}
.task-card__percent {
  flex: none;
  min-width: 36px;
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
  text-align: right;
}
.task-card__error {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  background: var(--color-error-soft);
  border-radius: var(--radius-md);
  color: var(--color-error-strong);
  font-size: var(--font-size-sm);
}
.task-card__error-code {
  margin-left: auto;
  opacity: 0.8;
  font-size: var(--font-size-xs);
}
.task-card__foot {
  display: flex;
  justify-content: flex-end;
}
.link {
  color: var(--color-primary);
  font-size: var(--font-size-sm);
}
.link:hover {
  text-decoration: underline;
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
@media (max-width: 640px) {
  .task-card__head {
    flex-direction: column;
  }
  .task-card__actions {
    align-self: flex-end;
  }
}
</style>
