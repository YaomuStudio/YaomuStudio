<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { t, tCount } from "@/i18n";
import { fallbackErrorNotice, messageNotice, renderNotice, type SafeNotice } from "@/i18n/notice";
import type { ProjectSummary } from "@/domain/types";
import PageHeader from "@/components/layout/PageHeader.vue";
import ProjectCard from "@/components/ProjectCard.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmDialog from "@/components/ui/YmDialog.vue";
import YmEmpty from "@/components/ui/YmEmpty.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmSkeleton from "@/components/ui/YmSkeleton.vue";
import YmField from "@/components/ui/YmField.vue";
import YmNotice from "@/components/ui/YmNotice.vue";
import YmTextInput from "@/components/ui/YmTextInput.vue";
import YmSelect from "@/components/ui/YmSelect.vue";
import { useProjectsStore } from "@/stores/projects";
import {
  projectStatusBucket,
  projectTypeKeyFromMode,
  sortProjectsByCreatedAsc,
  sortProjectsByRecent,
  type ProjectSortMode,
  type ProjectStatusBucket,
  type ProjectTypeKey,
} from "@/domain/projects";

const router = useRouter();
const route = useRoute();
const projects = useProjectsStore();

const SORT_STORAGE_KEY = "yaomu.projects.sort";
const PROJECT_TYPE_FILTERS: ProjectTypeKey[] = [
  "pipeline",
  "erase",
  "translate",
  "clone",
  "extract",
  "separation",
  "custom",
];
const PROJECT_STATUS_FILTERS: ProjectStatusBucket[] = [
  "pending",
  "running",
  "completed",
  "failed",
];
type ProjectTypeFilter = "all" | ProjectTypeKey;
type ProjectStatusFilter = "all" | ProjectStatusBucket;

function loadSortMode(): ProjectSortMode {
  try {
    return localStorage.getItem(SORT_STORAGE_KEY) === "created" ? "created" : "recent";
  } catch {
    return "recent";
  }
}

function saveSortMode(value: ProjectSortMode): void {
  try {
    localStorage.setItem(SORT_STORAGE_KEY, value);
  } catch {
    // 偏好持久化失败不影响排序本身。
  }
}

function typeFilterFromQuery(value: unknown): ProjectTypeFilter {
  return typeof value === "string" && PROJECT_TYPE_FILTERS.includes(value as ProjectTypeKey)
    ? (value as ProjectTypeKey)
    : "all";
}

function statusFilterFromQuery(value: unknown): ProjectStatusFilter {
  return typeof value === "string" && PROJECT_STATUS_FILTERS.includes(value as ProjectStatusBucket)
    ? (value as ProjectStatusBucket)
    : "all";
}

const error = ref<SafeNotice | null>(null);
const notice = ref<SafeNotice | null>(null);
const errorText = computed(() => renderNotice(error.value));
const noticeText = computed(() => renderNotice(notice.value));
const createOpen = ref(false);

/** 创建入口先选任务类型，再进入对应创建页；空白项目仍保留在下方。 */
const CREATE_TARGETS = [
  { key: "pipeline", labelKey: "workbench.type.pipeline", icon: "film", route: "/pipeline/new" },
  { key: "separation", labelKey: "workbench.type.separation", icon: "layers", route: "/tools/separate" },
  { key: "extract", labelKey: "workbench.type.extract", icon: "subtitles", route: "/tools/extract" },
  { key: "erase", labelKey: "workbench.type.erase", icon: "eraser", route: "/tools/erase" },
  { key: "translate", labelKey: "workbench.type.translate", icon: "globe", route: "/tools/translate" },
  { key: "clone", labelKey: "workbench.type.clone", icon: "mic", route: "/voice-clone" },
  { key: "custom", labelKey: "workbench.type.custom", icon: "link", route: "/tools/custom" },
] as const;

function startFromType(target: (typeof CREATE_TARGETS)[number]): void {
  createOpen.value = false;
  void router.push(target.route);
}

const createName = ref("");
const creating = ref(false);
const deleteTarget = ref<ProjectSummary | null>(null);
const renameTarget = ref<ProjectSummary | null>(null);
const renameName = ref("");
const renameError = ref<SafeNotice | null>(null);
const renameErrorText = computed(() => renderNotice(renameError.value));
const renaming = ref(false);

const initialQuery = typeof route.query.q === "string" ? route.query.q : "";
const searchDraft = ref(initialQuery);
const searchQuery = ref(initialQuery);
const typeFilter = ref<ProjectTypeFilter>(typeFilterFromQuery(route.query.type));
const statusFilter = ref<ProjectStatusFilter>(statusFilterFromQuery(route.query.status));
const sortMode = ref<ProjectSortMode>(loadSortMode());
const searchInput = ref<HTMLInputElement | null>(null);
let noticeTimer: number | undefined;

const sortedProjects = computed<ProjectSummary[]>(() =>
  sortMode.value === "created"
    ? sortProjectsByCreatedAsc(projects.items)
    : sortProjectsByRecent(projects.items),
);

const sortOptions = computed(() => [
  { value: "recent", label: t("projects.sort.recent") },
  { value: "created", label: t("projects.sort.created") },
]);

const typeOptions = computed(() => [
  { value: "all", label: t("projects.filter.allTypes") },
  ...PROJECT_TYPE_FILTERS.map((value) => ({
    value,
    label: t(`workbench.type.${value}`),
  })),
]);

const statusOptions = computed(() => [
  { value: "all", label: t("projects.filter.allStatuses") },
  ...PROJECT_STATUS_FILTERS.map((value) => ({
    value,
    label: t(`projects.filter.status.${value}`),
  })),
]);

function normalizeSearch(value: string): string {
  return value.trim().toLocaleLowerCase().replace(/^#+/, "");
}

const hasSearch = computed(() => normalizeSearch(searchQuery.value).length > 0);
const hasFacetFilters = computed(
  () => typeFilter.value !== "all" || statusFilter.value !== "all",
);
const hasFilters = computed(() => hasSearch.value || hasFacetFilters.value);

const filteredProjects = computed<ProjectSummary[]>(() => {
  const query = normalizeSearch(searchQuery.value);
  return sortedProjects.value.filter((project) => {
    if (
      typeFilter.value !== "all" &&
      projectTypeKeyFromMode(project.modeType) !== typeFilter.value
    ) {
      return false;
    }
    if (
      statusFilter.value !== "all" &&
      projectStatusBucket(project.latestRunStatus) !== statusFilter.value
    ) {
      return false;
    }
    if (!query) return true;
    // 单串匹配；不按空格拆成多词 AND。
    return [project.name, project.id, project.episodeId ?? ""].some((value) =>
      normalizeSearch(value).includes(query),
    );
  });
});

watch(sortMode, (value) => saveSortMode(value));

function applySearch(): void {
  searchQuery.value = searchDraft.value.trim();
}

function clearSearch(): void {
  searchDraft.value = "";
  searchQuery.value = "";
  searchInput.value?.focus();
}

function clearFilters(): void {
  searchDraft.value = "";
  searchQuery.value = "";
  typeFilter.value = "all";
  statusFilter.value = "all";
  searchInput.value?.focus();
}

function setTypeFilter(value: string): void {
  if (PROJECT_TYPE_FILTERS.includes(value as ProjectTypeKey)) {
    typeFilter.value = value as ProjectTypeKey;
  } else {
    typeFilter.value = "all";
  }
}

function setStatusFilter(value: string): void {
  if (PROJECT_STATUS_FILTERS.includes(value as ProjectStatusBucket)) {
    statusFilter.value = value as ProjectStatusBucket;
  } else {
    statusFilter.value = "all";
  }
}

function onSortChange(value: string): void {
  sortMode.value = value === "created" ? "created" : "recent";
}

function onSearchEscape(): void {
  if (searchDraft.value || searchQuery.value) clearSearch();
  else searchInput.value?.blur();
}

watch([searchQuery, typeFilter, statusFilter], () => {
  const currentQ = typeof route.query.q === "string" ? route.query.q : "";
  const currentType = typeof route.query.type === "string" ? route.query.type : "all";
  const currentStatus = typeof route.query.status === "string" ? route.query.status : "all";
  if (
    searchQuery.value === currentQ &&
    typeFilter.value === currentType &&
    statusFilter.value === currentStatus
  ) {
    return;
  }
  const query = { ...route.query };
  if (searchQuery.value) query.q = searchQuery.value;
  else delete query.q;
  if (typeFilter.value !== "all") query.type = typeFilter.value;
  else delete query.type;
  if (statusFilter.value !== "all") query.status = statusFilter.value;
  else delete query.status;
  void router.replace({ query });
});

watch(
  () => [route.query.q, route.query.type, route.query.status],
  ([q, type, status]) => {
    const nextQuery = typeof q === "string" ? q : "";
    const nextType = typeFilterFromQuery(type);
    const nextStatus = statusFilterFromQuery(status);
    if (nextQuery !== searchQuery.value) searchQuery.value = nextQuery;
    if (nextQuery !== searchDraft.value) searchDraft.value = nextQuery;
    if (nextType !== typeFilter.value) typeFilter.value = nextType;
    if (nextStatus !== statusFilter.value) statusFilter.value = nextStatus;
  },
);

onMounted(() => {
  void (async () => {
    try {
      await projects.load();
    } catch (value) {
      error.value = fallbackErrorNotice(value, "projects.error.generic");
    }
  })();
});

onBeforeUnmount(() => {
  if (noticeTimer !== undefined) window.clearTimeout(noticeTimer);
});

function flashNotice(): void {
  if (noticeTimer !== undefined) window.clearTimeout(noticeTimer);
  noticeTimer = window.setTimeout(() => {
    notice.value = null;
  }, 5000);
}

async function openProject(project: { id: string }): Promise<void> {
  try {
    await projects.open(project.id);
    await router.push(`/projects/${project.id}`);
  } catch (value) {
    error.value = fallbackErrorNotice(value, "projects.error.generic");
  }
}

async function createProject(): Promise<void> {
  if (creating.value) return;
  const name = createName.value.trim();
  if (!name) {
    error.value = messageNotice("projects.new.emptyError");
    return;
  }
  creating.value = true;
  error.value = null;
  try {
    const project = await projects.create(name);
    createOpen.value = false;
    createName.value = "";
    notice.value = messageNotice("projects.created", { name: project.name });
    flashNotice();
    await router.push(`/projects/${project.id}`);
  } catch (value) {
    error.value = fallbackErrorNotice(value, "projects.error.generic");
  } finally {
    creating.value = false;
  }
}

function correctProject(id: string): void {
  void router.push(`/manual-correction/${id}`);
}

function requestDelete(project: ProjectSummary): void {
  deleteTarget.value = project;
}

function requestRename(project: ProjectSummary): void {
  renameTarget.value = project;
  renameName.value = project.name;
  renameError.value = null;
}

function onRenameDialogUpdate(open: boolean): void {
  if (!open) {
    renameTarget.value = null;
    renameError.value = null;
  }
}

async function confirmRename(): Promise<void> {
  const target = renameTarget.value;
  if (!target || renaming.value) return;
  const name = renameName.value.trim();
  renameError.value = null;
  if (!name) {
    renameError.value = messageNotice("projects.rename.empty");
    return;
  }
  if ([...name].length > 30) {
    renameError.value = messageNotice("projects.rename.tooLong");
    return;
  }
  renaming.value = true;
  try {
    await projects.rename(target.id, name, target.editVersion);
    renameTarget.value = null;
    notice.value = messageNotice("projects.renamed", { name });
    flashNotice();
  } catch (value) {
    renameError.value = fallbackErrorNotice(value, "projects.error.generic");
  } finally {
    renaming.value = false;
  }
}

function onDeleteDialogUpdate(open: boolean): void {
  if (!open) deleteTarget.value = null;
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value;
  deleteTarget.value = null;
  if (!target) return;
  try {
    await projects.remove(target.id);
    notice.value = messageNotice("projects.deleted", { name: target.name });
    flashNotice();
  } catch (value) {
    error.value = fallbackErrorNotice(value, "projects.error.generic");
  }
}

function openFolder(name: string): void {
  notice.value = messageNotice("projects.folder.mock", { name });
  flashNotice();
}
</script>

<template>
  <div class="page">
    <PageHeader :title="t('projects.title')" :description="t('projects.subtitle')" icon="folder">
      <template #actions>
        <YmButton
          variant="primary"
          icon="plus"
          data-testid="project-create"
          @click="createOpen = true"
        >
          {{ t("projects.new.action") }}
        </YmButton>
      </template>
    </PageHeader>

    <YmNotice v-if="noticeText" tone="success">{{ noticeText }}</YmNotice>
    <YmNotice v-if="errorText" tone="error">{{ errorText }}</YmNotice>



    <div class="section-head">
      <h2 class="section-title">{{ t("projects.list.title") }}</h2>
      <span class="muted">
        <bdi v-if="hasFilters">{{
          t("projects.search.count", {
            shown: filteredProjects.length,
            total: sortedProjects.length,
          })
        }}</bdi>
        <bdi v-else>{{ tCount("projects.list.count", sortedProjects.length) }}</bdi>
      </span>
    </div>

    <div v-if="projects.items.length" class="list-toolbar" role="search">
      <div class="project-search">
        <YmIcon name="search" :size="16" class="project-search__icon" />
        <input
          ref="searchInput"
          v-model="searchDraft"
          class="project-search__input"
          type="text"
          autocomplete="off"
          spellcheck="false"
          data-testid="project-search-input"
          :placeholder="t('projects.search.placeholder')"
          :aria-label="t('projects.search.placeholder')"
          @keydown.enter.prevent="applySearch"
          @keydown.esc.prevent="onSearchEscape"
        />
        <button
          v-if="searchDraft || searchQuery"
          type="button"
          class="project-search__clear"
          data-testid="project-search-clear"
          :aria-label="t('projects.search.clear')"
          :title="t('projects.search.clear')"
          @click="clearSearch"
        >
          <YmIcon name="x" :size="14" />
        </button>
      </div>
      <YmSelect
        class="filter-select"
        data-testid="project-sort"
        :model-value="sortMode"
        :options="sortOptions"
        :aria-label="t('projects.sort.label')"
        @update:model-value="onSortChange"
      />
      <YmSelect
        class="filter-select"
        data-testid="project-type-filter"
        :model-value="typeFilter"
        :options="typeOptions"
        :aria-label="t('projects.filter.type')"
        @update:model-value="setTypeFilter"
      />
      <YmSelect
        class="filter-select"
        data-testid="project-status-filter"
        :model-value="statusFilter"
        :options="statusOptions"
        :aria-label="t('projects.filter.status')"
        @update:model-value="setStatusFilter"
      />
      <YmButton
        v-if="hasFilters"
        size="sm"
        variant="secondary"
        data-testid="project-filter-clear"
        @click="clearFilters"
      >
        {{ t("projects.filter.clear") }}
      </YmButton>
    </div>

    <template v-if="projects.loading && !projects.items.length">
      <p class="visually-hidden" role="status">{{ t("projects.loading") }}</p>
      <div class="project-grid" aria-hidden="true">
        <div v-for="n in 3" :key="n" class="project-skeleton">
          <YmSkeleton class="project-skeleton__cover" width="100%" height="auto" radius="0" />
          <div class="project-skeleton__body">
            <YmSkeleton width="62%" height="16px" />
            <YmSkeleton width="86%" height="11px" />
            <YmSkeleton width="52%" height="11px" />
            <YmSkeleton width="70%" height="11px" />
          </div>
        </div>
      </div>
    </template>
    <YmEmpty
      v-else-if="!sortedProjects.length"
      fill
      icon="folder"
      :title="t('projects.empty.title')"
      :description="t('projects.empty.desc')"
    />
    <YmEmpty
      v-else-if="!filteredProjects.length"
      fill
      icon="search"
      :title="t('projects.search.emptyTitle')"
      :description="
        hasSearch
          ? t('projects.search.emptyDesc', { query: searchQuery.trim() })
          : t('projects.filter.emptyDesc')
      "
    >
      <template #action>
        <YmButton variant="secondary" @click="hasFacetFilters ? clearFilters() : clearSearch()">
          {{ hasFacetFilters ? t("projects.filter.clear") : t("projects.search.clear") }}
        </YmButton>
      </template>
    </YmEmpty>
    <div v-else class="project-grid" data-testid="project-list">
      <ProjectCard
        v-for="project in filteredProjects"
        :key="project.id"
        :project="project"
        correction-available
        @open="openProject(project)"
        @correct="correctProject(project.id)"
        @rename="requestRename(project)"
        @delete="requestDelete(project)"
      />
    </div>

    <YmDialog
      :model-value="createOpen"
      :title="t('projects.new.title')"
      @update:model-value="createOpen = $event"
    >
      <div class="create-types" data-testid="project-create-types">
        <button
          v-for="target in CREATE_TARGETS"
          :key="target.key"
          type="button"
          class="create-type"
          :data-testid="`project-create-type-${target.key}`"
          @click="startFromType(target)"
        >
          <span class="create-type__icon"><YmIcon :name="target.icon" :size="18" /></span>
          <span class="create-type__label">{{ t(target.labelKey) }}</span>
        </button>
      </div>
      <div class="create-divider" aria-hidden="true"></div>
      <YmField :label="t('projects.new.label')">
        <YmTextInput
          v-model="createName"
          data-testid="project-create-input"
          :placeholder="t('projects.new.placeholder')"
          :disabled="creating"
          @keyup.enter="createProject"
        />
      </YmField>
      <template #footer="{ close }">
        <YmButton variant="secondary" @click="close()">{{ t("common.cancel") }}</YmButton>
        <YmButton
          variant="primary"
          data-testid="project-create-confirm"
          :loading="creating"
          :disabled="!createName.trim()"
          @click="createProject"
        >
          {{ t("projects.new.action") }}
        </YmButton>
      </template>
    </YmDialog>

    <YmDialog
      :model-value="renameTarget !== null"
      :title="t('projects.rename.title')"
      @update:model-value="onRenameDialogUpdate"
    >
      <YmField :label="t('projects.rename.label')">
        <YmTextInput
          v-model="renameName"
          data-testid="project-rename-input"
          :placeholder="t('projects.rename.placeholder')"
          :disabled="renaming"
          @keyup.enter="confirmRename"
        />
      </YmField>
      <p v-if="renameErrorText" class="rename-error" role="alert">{{ renameErrorText }}</p>
      <template #footer="{ close }">
        <YmButton variant="secondary" @click="close()">{{ t("common.cancel") }}</YmButton>
        <YmButton
          variant="primary"
          data-testid="project-rename-confirm"
          :loading="renaming"
          :disabled="!renameName.trim()"
          @click="confirmRename"
        >
          {{ t("projects.rename.confirm") }}
        </YmButton>
      </template>
    </YmDialog>

    <YmDialog
      :model-value="deleteTarget !== null"
      :title="t('projects.delete.title')"
      :confirm-text="t('projects.delete.confirm')"
      :cancel-text="t('common.cancel')"
      danger
      @update:model-value="onDeleteDialogUpdate"
      @confirm="confirmDelete"
    >
      <p>{{ deleteTarget ? t("projects.delete.body", { name: deleteTarget.name }) : "" }}</p>
    </YmDialog>
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
.section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  flex-wrap: wrap;
}
.section-controls {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: var(--space-4);
  flex-wrap: wrap;
}
.section-title {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--font-size-lg);
  font-weight: 600;
}
.section-title::before {
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
.list-toolbar {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.list-toolbar .project-search {
  width: 300px;
}
.filter-select {
  width: 160px;
  flex: none;
}
.project-search {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 260px;
  height: 34px;
  padding: 0 8px 0 10px;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  color: var(--color-text-faint);
}
.project-search:focus-within {
  border-color: var(--color-primary);
  box-shadow: var(--focus-ring);
}
.project-search__icon {
  flex: none;
}
.project-search__input {
  flex: 1;
  min-width: 0;
  border: 0;
  outline: none;
  background: transparent;
  color: var(--color-text);
  font: inherit;
}
.project-search__input:focus-visible,
.project-search__clear:focus-visible {
  /* 焦点环由外层 :focus-within 统一绘制，避免内外两层同心边框。 */
  box-shadow: none;
}
.project-search__input::placeholder {
  color: var(--color-text-faint);
}
.project-search__clear {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 24px;
  height: 24px;
  padding: 0;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-faint);
  cursor: pointer;
}
.project-search__clear:hover {
  background: var(--color-surface-2);
  color: var(--color-text);
}
.project-skeleton {
  overflow: hidden;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}
.project-skeleton__cover {
  aspect-ratio: 16 / 9;
}
.project-skeleton__body {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 14px;
}
.project-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 14px;
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
@media (max-width: 1180px) {
  .project-skeleton {
  overflow: hidden;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}
.project-skeleton__cover {
  aspect-ratio: 16 / 9;
}
.project-skeleton__body {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 14px;
}
.project-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (min-width: 761px) and (max-width: 1220px) {
  /* 中等宽度：两列网格，避免最后一个筛选项单独换行。 */
  .list-toolbar {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .list-toolbar .project-search,
  .filter-select {
    width: 100%;
  }
  .list-toolbar > .ym-btn {
    grid-column: 1 / -1;
    justify-self: start;
  }
}
@media (max-width: 760px) {
  .list-toolbar .project-search,
  .filter-select {
    width: 100%;
  }
  .project-skeleton {
  overflow: hidden;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}
.project-skeleton__cover {
  aspect-ratio: 16 / 9;
}
.project-skeleton__body {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 14px;
}
.project-grid {
    grid-template-columns: 1fr;
  }
}

.create-types {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}
.create-type {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 44px;
  padding: 8px 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: var(--color-surface);
  color: var(--color-text);
  font-size: var(--font-size-sm);
  text-align: start;
  transition:
    border-color var(--motion-duration-snap) var(--motion-ease-snap),
    background var(--motion-duration-snap) var(--motion-ease-snap);
}
.create-type:hover {
  border-color: color-mix(in srgb, var(--color-primary) 40%, var(--color-border));
  background: var(--color-surface-2);
}
.create-type__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  flex: none;
  border-radius: var(--radius-md);
  background: var(--color-primary-soft);
  color: var(--color-primary);
}
.create-type__label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.create-divider {
  height: 1px;
  margin: var(--space-4) 0 var(--space-3);
  background: var(--color-border);
}
</style>
