<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { t } from "@/i18n";
import type { ProjectSummary, RunStatus } from "@/domain/types";
import { evaluateAccess, type AccessKey } from "@/domain/capabilityAccess";
import PageHeader from "@/components/layout/PageHeader.vue";
import ProjectCard from "@/components/ProjectCard.vue";
import YmNotice from "@/components/ui/YmNotice.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmDialog from "@/components/ui/YmDialog.vue";
import YmField from "@/components/ui/YmField.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmSkeleton from "@/components/ui/YmSkeleton.vue";
import YmTextInput from "@/components/ui/YmTextInput.vue";
import WorkbenchToolWatermark from "@/components/WorkbenchToolWatermark.vue";
import { useCapabilitiesStore } from "@/stores/capabilities";
import { useProjectsStore } from "@/stores/projects";
import { sortProjectsByRecent } from "@/domain/projects";

interface WorkbenchTool {
  key: string;
  titleKey: string;
  descKey: string;
  iconName: string;
  route: string;
  badge?: "hot" | "new";
  actionKey: string;
  accessKey?: AccessKey;
}

const RECOMMENDED_TOOLS: WorkbenchTool[] = [
  {
    key: "pipeline",
    titleKey: "workbench.tool.pipeline.title",
    descKey: "workbench.tool.pipeline.desc",
    iconName: "film",
    route: "/pipeline/new",
    badge: "hot",
    actionKey: "workbench.tool.try",
    accessKey: "pipeline",
  },
  {
    key: "erase",
    titleKey: "workbench.tool.erase.title",
    descKey: "workbench.tool.erase.desc",
    iconName: "eraser",
    route: "/tools/erase",
    badge: "hot",
    actionKey: "workbench.tool.open",
    accessKey: "tool-erase",
  },
  {
    key: "custom",
    titleKey: "workbench.tool.custom.title",
    descKey: "workbench.tool.custom.desc",
    iconName: "link",
    route: "/tools/custom",
    badge: "new",
    actionKey: "workbench.tool.open",
    accessKey: "tool-custom",
  },
];

const AI_TOOLS: WorkbenchTool[] = [
  {
    key: "separation",
    titleKey: "workbench.tool.separation.title",
    descKey: "workbench.tool.separation.desc",
    iconName: "layers",
    route: "/tools/separate",
    actionKey: "workbench.tool.open",
    accessKey: "tool-separate",
  },
  {
    key: "clone",
    titleKey: "workbench.tool.clone.title",
    descKey: "workbench.tool.clone.desc",
    iconName: "mic",
    route: "/voice-clone",
    actionKey: "workbench.tool.open",
    accessKey: "voice-clone",
  },
  {
    key: "translate",
    titleKey: "workbench.tool.translate.title",
    descKey: "workbench.tool.translate.desc",
    iconName: "globe",
    route: "/tools/translate",
    actionKey: "workbench.tool.open",
    accessKey: "tool-translate",
  },
  {
    key: "extract",
    titleKey: "workbench.tool.extract.title",
    descKey: "workbench.tool.extract.desc",
    iconName: "subtitles",
    route: "/tools/extract",
    actionKey: "workbench.tool.open",
    accessKey: "tool-extract",
  },
];

const router = useRouter();
const projects = useProjectsStore();
const caps = useCapabilitiesStore();

const error = ref("");
const notice = ref("");
const deleteTarget = ref<ProjectSummary | null>(null);
const renameTarget = ref<ProjectSummary | null>(null);
const renameName = ref("");
const renameError = ref("");
const renaming = ref(false);
let noticeTimer: number | undefined;

const UNFINISHED_RUN_STATUSES: RunStatus[] = [
  "DRAFT",
  "QUEUED",
  "RUNNING",
  "PAUSE_REQUESTED",
  "PAUSED",
  "PARTIAL",
];

const unfinishedProjects = computed<ProjectSummary[]>(() =>
  projects.items.filter(
    (project) =>
      project.latestRunStatus !== undefined &&
      UNFINISHED_RUN_STATUSES.includes(project.latestRunStatus),
  ),
);
const recentProjects = computed<ProjectSummary[]>(() =>
  sortProjectsByRecent(projects.items).slice(0, 4),
);
const visibleRecommended = computed(() =>
  RECOMMENDED_TOOLS.filter(
    (tool) => tool.accessKey === undefined || evaluateAccess(caps.slots, tool.accessKey).level !== "hidden",
  ),
);
const visibleAiTools = computed(() =>
  AI_TOOLS.filter(
    (tool) => tool.accessKey === undefined || evaluateAccess(caps.slots, tool.accessKey).level !== "hidden",
  ),
);

onMounted(() => {
  void caps.load().catch(() => undefined);
  void (async () => {
    try {
      await projects.load();
    } catch {
      error.value = t("workbench.error.load");
    }
  })();
});

onBeforeUnmount(() => {
  if (noticeTimer !== undefined) window.clearTimeout(noticeTimer);
});

function flashNotice(): void {
  if (noticeTimer !== undefined) window.clearTimeout(noticeTimer);
  noticeTimer = window.setTimeout(() => {
    notice.value = "";
  }, 5000);
}

async function go(path: string): Promise<void> {
  await router.push(path);
}

function openTool(tool: WorkbenchTool): void {
  void go(tool.route);
}

async function openProject(project: ProjectSummary): Promise<void> {
  try {
    await projects.open(project.id);
    await router.push(`/projects/${project.id}`);
  } catch {
    error.value = t("workbench.error.open");
  }
}

function correctProject(project: ProjectSummary): void {
  void router.push(`/manual-correction/${project.id}`);
}

function requestDelete(project: ProjectSummary): void {
  deleteTarget.value = project;
}

function requestRename(project: ProjectSummary): void {
  renameTarget.value = project;
  renameName.value = project.name;
  renameError.value = "";
}

function onRenameDialogUpdate(open: boolean): void {
  if (!open) {
    renameTarget.value = null;
    renameError.value = "";
  }
}

async function confirmRename(): Promise<void> {
  const target = renameTarget.value;
  if (!target || renaming.value) return;
  const name = renameName.value.trim();
  renameError.value = "";
  if (!name) {
    renameError.value = t("projects.rename.empty");
    return;
  }
  if ([...name].length > 30) {
    renameError.value = t("projects.rename.tooLong");
    return;
  }
  renaming.value = true;
  try {
    await projects.rename(target.id, name, target.editVersion);
    renameTarget.value = null;
    notice.value = t("projects.renamed", { name });
    flashNotice();
  } catch {
    renameError.value = t("projects.error.generic");
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
    notice.value = t("projects.deleted", { name: target.name });
    flashNotice();
  } catch {
    error.value = t("projects.error.generic");
  }
}
</script>

<template>
  <div class="page">
    <PageHeader :title="t('workbench.title')" :description="t('workbench.subtitle')" icon="home">
      <template #actions>
        <div class="wb-stats">
          <div class="wb-stat">
            <span class="wb-stat__value">
              <YmSkeleton v-if="projects.loading && !projects.items.length" width="26px" height="24px" radius="6px" />
              <template v-else>{{ projects.items.length }}</template>
            </span>
            <span class="wb-stat__label">{{ t("workbench.stats.projects") }}</span>
          </div>
          <div class="wb-stat">
            <span class="wb-stat__value">
              <YmSkeleton v-if="projects.loading && !projects.items.length" width="26px" height="24px" radius="6px" />
              <template v-else>{{ unfinishedProjects.length }}</template>
            </span>
            <span class="wb-stat__label">{{ t("workbench.stats.unfinished") }}</span>
          </div>
        </div>
      </template>
    </PageHeader>

    <YmNotice v-if="error" tone="error">{{ error }}</YmNotice>
    <YmNotice v-if="notice" tone="success">{{ notice }}</YmNotice>

    <section class="wb-section">
      <h2 class="section__title">{{ t("workbench.recommended") }}</h2>
      <div class="recommend-grid">
        <button
          v-for="tool in visibleRecommended"
          :key="tool.key"
          type="button"
          class="recommend-card"
          :class="{ 'recommend-card--feature': tool.key === 'pipeline' }"
          @click="openTool(tool)"
        >
          <span class="recommend-card__icon">
            <YmIcon :name="tool.iconName" :size="22" />
          </span>
          <span
            v-if="tool.badge"
            class="recommend-badge"
            :class="`recommend-badge--${tool.badge}`"
          >
            {{ t(`workbench.tool.${tool.badge}`) }}
          </span>
          <span class="recommend-card__body">
            <span class="recommend-card__title">{{ t(tool.titleKey) }}</span>
            <span class="recommend-card__desc">{{ t(tool.descKey) }}</span>
            <span class="recommend-card__action">
              {{ t(tool.actionKey) }}
              <span aria-hidden="true">→</span>
            </span>
          </span>
          <WorkbenchToolWatermark
            :kind="tool.key === 'pipeline' ? 'pipeline' : tool.key === 'erase' ? 'erase' : 'custom'"
            class="recommend-card__watermark"
          />
        </button>
      </div>
    </section>

    <section class="wb-section">
      <h2 class="section__title">{{ t("workbench.aiTools") }}</h2>
      <div class="ai-grid">
        <button
          v-for="tool in visibleAiTools"
          :key="tool.key"
          type="button"
          class="ai-tool"
          @click="openTool(tool)"
        >
          <span class="ai-tool__icon">
            <YmIcon :name="tool.iconName" :size="20" />
          </span>
          <span class="ai-tool__body">
            <span class="ai-tool__title">{{ t(tool.titleKey) }}</span>
            <span class="ai-tool__desc">{{ t(tool.descKey) }}</span>
          </span>
          <YmIcon class="ai-tool__chev" name="chevron-right" :size="14" />
        </button>
      </div>
    </section>

    <section class="wb-section">
      <div class="section-head">
        <h2 class="section__title">{{ t("workbench.recent.title") }}</h2>
        <RouterLink to="/projects" class="view-more">
          {{ t("workbench.recent.viewAll") }}
          <YmIcon name="chevron-right" :size="14" />
        </RouterLink>
      </div>

      <template v-if="projects.loading && !recentProjects.length">
        <p class="visually-hidden" role="status">{{ t("workbench.loading") }}</p>
        <div class="recent-grid" aria-hidden="true">
          <div v-for="i in 4" :key="i" class="recent-skeleton">
            <YmSkeleton class="recent-skeleton__cover" width="100%" height="118px" radius="0" />
            <div class="recent-skeleton__body">
              <YmSkeleton width="70%" height="10px" />
              <YmSkeleton width="42%" height="10px" />
            </div>
          </div>
        </div>
      </template>

      <div v-else-if="!recentProjects.length" class="recent-empty">
        <span class="recent-empty__icon"><YmIcon name="folder" :size="28" /></span>
        <p class="recent-empty__title">{{ t("workbench.recent.empty.title") }}</p>
        <p class="recent-empty__desc">{{ t("workbench.recent.empty.desc") }}</p>
      </div>

      <div v-else class="recent-grid">
        <ProjectCard
          v-for="project in recentProjects"
          :key="project.id"
          :project="project"
          correction-available
          @open="openProject(project)"
          @correct="correctProject(project)"
          @rename="requestRename(project)"
          @delete="requestDelete(project)"
        />
      </div>
    </section>

    <YmDialog
      :model-value="renameTarget !== null"
      :title="t('projects.rename.title')"
      @update:model-value="onRenameDialogUpdate"
    >
      <YmField :label="t('projects.rename.label')">
        <YmTextInput
          v-model="renameName"
          data-testid="workbench-project-rename-input"
          :placeholder="t('projects.rename.placeholder')"
          :disabled="renaming"
          @keyup.enter="confirmRename"
        />
      </YmField>
      <p v-if="renameError" class="rename-error" role="alert">{{ renameError }}</p>
      <template #footer="{ close }">
        <YmButton variant="secondary" @click="close()">{{ t("common.cancel") }}</YmButton>
        <YmButton
          variant="primary"
          data-testid="workbench-project-rename-confirm"
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
  gap: 22px;
  max-width: 1240px;
  margin: 0 auto;
  padding: 24px var(--page-gutter) var(--page-bottom);
}
.wb-stats {
  display: flex;
  align-items: flex-start;
}
.wb-stat {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  padding: 0 24px;
  border-left: 1px solid var(--color-border);
}
.wb-stat:first-child {
  padding-left: 0;
  border-left: none;
}
.wb-stat:last-child {
  padding-right: 0;
}
.wb-stat__value {
  display: inline-flex;
  align-items: center;
  min-height: 31px;
  font-size: 22px;
  font-weight: 700;
  letter-spacing: -0.01em;
  font-variant-numeric: tabular-nums;
}
.wb-stat__label {
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
}
.wb-section {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.section__title {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--font-size-lg);
  font-weight: 600;
}
.section__title::before {
  content: "";
  width: 4px;
  height: 14px;
  flex: none;
  border-radius: var(--radius-pill);
  background: var(--gradient-primary);
}
.section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
}
.view-more {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  text-decoration: none;
  transition: color var(--motion-duration-base) var(--motion-ease-out);
}
.view-more:hover {
  color: var(--color-primary);
  text-decoration: none;
}
.recommend-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 14px;
}
.recommend-card {
  position: relative;
  display: flex;
  flex-direction: column;
  min-height: 184px;
  overflow: hidden;
  padding: 22px;
  text-align: left;
  border-radius: var(--radius-lg);
  border: 1px solid var(--color-border);
  background: var(--color-surface);
  box-shadow: var(--shadow-sm);
  transition: transform var(--motion-duration-base) var(--motion-ease-out), border-color var(--motion-duration-base) var(--motion-ease-out), box-shadow var(--motion-duration-base) var(--motion-ease-out);
}
.recommend-card:hover {
  transform: translateY(-2px);
  border-color: color-mix(in srgb, var(--color-primary) 32%, var(--color-border));
  box-shadow: var(--shadow-md);
}
.recommend-card--feature {
  border: none;
  background: var(--feature-gradient);
  color: var(--color-on-primary);
  box-shadow: 0 14px 34px color-mix(in srgb, var(--color-primary) 24%, transparent);
}
.recommend-card--feature::before {
  content: "";
  position: absolute;
  inset: 0;
  background:
    radial-gradient(240px 120px at 85% -10%, rgba(255, 255, 255, 0.18), transparent 70%),
    radial-gradient(200px 140px at -10% 110%, rgba(0, 212, 255, 0.25), transparent 70%);
  pointer-events: none;
}
.recommend-card--feature:hover {
  border-color: transparent;
  box-shadow: 0 18px 42px color-mix(in srgb, var(--color-primary) 30%, transparent);
}
[data-theme="dark"] .recommend-card--feature {
  box-shadow: 0 14px 34px rgba(0, 0, 0, 0.32);
}
[data-theme="dark"] .recommend-card--feature:hover {
  box-shadow: 0 18px 42px rgba(0, 0, 0, 0.4);
}
.recommend-card__icon {
  position: relative;
  z-index: 1;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 42px;
  height: 42px;
  flex: none;
  border-radius: var(--radius-card);
  background: var(--color-primary-soft);
  color: var(--color-primary);
}
.recommend-card--feature .recommend-card__icon {
  border: 1px solid rgba(255, 255, 255, 0.22);
  background: rgba(255, 255, 255, 0.16);
  color: var(--color-on-primary);
}
.recommend-card__body {
  position: relative;
  z-index: 1;
  display: flex;
  flex: 1;
  flex-direction: column;
  padding-top: 14px;
}
.recommend-card__title {
  font-size: var(--font-size-page-title);
  font-weight: 600;
  color: var(--color-text);
}
.recommend-card__desc {
  max-width: 360px;
  margin-top: 8px;
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  line-height: 1.6;
}
.recommend-card__action {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-top: auto;
  padding-top: 16px;
  color: var(--color-primary-ink);
  font-size: var(--font-size-sm);
  font-weight: 500;
  transition: gap var(--motion-duration-base) var(--motion-ease-out);
}
.recommend-card:hover .recommend-card__action {
  gap: 8px;
}
.recommend-card--feature .recommend-card__title,
.recommend-card--feature .recommend-card__action {
  color: var(--color-on-primary);
}
.recommend-card--feature .recommend-card__desc {
  color: rgba(255, 255, 255, 0.78);
}
.recommend-card__watermark {
  position: absolute;
  right: 10px;
  bottom: 8px;
  z-index: 0;
  color: var(--color-primary-ink);
  opacity: 0.22;
  pointer-events: none;
}
.recommend-card--feature .recommend-card__watermark {
  color: var(--color-on-primary);
  opacity: 0.32;
}
.recommend-badge {
  position: absolute;
  top: 12px;
  right: 12px;
  z-index: 2;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 22px;
  padding: 0 8px;
  border: 1px solid transparent;
  border-radius: var(--radius-pill);
  font-size: var(--font-size-xs);
  font-weight: 600;
  line-height: 1;
  letter-spacing: 0.01em;
  white-space: nowrap;
  box-shadow: var(--shadow-sm);
}
.recommend-badge::before {
  content: "";
  width: 4px;
  height: 4px;
  flex: none;
  border-radius: 50%;
  background: currentColor;
}
.recommend-badge--hot {
  border-color: color-mix(in srgb, var(--color-error) 22%, transparent);
  background: color-mix(in srgb, var(--color-error) 10%, var(--color-surface));
  color: var(--color-error-strong);
}
.recommend-badge--new {
  border-color: color-mix(in srgb, var(--color-primary) 20%, transparent);
  background: color-mix(in srgb, var(--color-primary) 10%, var(--color-surface));
  color: var(--color-primary-ink);
}
.recommend-card--feature .recommend-badge {
  border-color: color-mix(in srgb, var(--color-error) 24%, transparent);
  background: color-mix(in srgb, var(--color-surface) 92%, transparent);
  color: var(--color-error-strong);
  box-shadow: var(--shadow-sm);
  backdrop-filter: blur(6px);
}
.recommend-card--feature .recommend-badge--new {
  border-color: color-mix(in srgb, var(--color-primary) 24%, transparent);
  color: var(--color-primary-ink);
}
.ai-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--space-3);
}
.ai-tool {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 16px;
  text-align: left;
  border-radius: var(--radius-card);
  border: 1px solid var(--color-border);
  background: var(--color-surface);
  box-shadow: var(--shadow-sm);
  transition: transform var(--motion-duration-base) var(--motion-ease-out), border-color var(--motion-duration-base) var(--motion-ease-out), box-shadow var(--motion-duration-base) var(--motion-ease-out);
}
.ai-tool:hover {
  transform: translateY(-2px);
  border-color: color-mix(in srgb, var(--color-primary) 32%, var(--color-border));
  box-shadow: var(--shadow-primary);
}
.ai-tool__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 42px;
  height: 42px;
  flex: none;
  border-radius: var(--radius-control);
  background: var(--color-primary-soft);
  color: var(--color-primary);
  transition: background var(--motion-duration-base) var(--motion-ease-out), color var(--motion-duration-base) var(--motion-ease-out);
}
.ai-tool:hover .ai-tool__icon {
  background: var(--gradient-primary);
  color: var(--color-on-primary);
}
.ai-tool__body {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}
.ai-tool__title {
  color: var(--color-text);
  font-size: var(--font-size-md);
  font-weight: 600;
}
.ai-tool__desc {
  margin-top: 2px;
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
  line-height: 1.5;
}
.ai-tool__chev {
  flex: none;
  color: var(--color-text-faint);
  transition: color var(--motion-duration-base) var(--motion-ease-out), transform var(--motion-duration-base) var(--motion-ease-out);
}
.ai-tool:hover .ai-tool__chev {
  color: var(--color-primary);
  transform: translateX(2px);
}
.recent-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 14px;
}
.recent-skeleton {
  overflow: hidden;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}
.recent-skeleton__cover {
  height: 118px;
}
.recent-skeleton__body {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 14px;
}
.recent-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--space-2);
  min-height: 320px;
  padding: 40px 24px;
  text-align: center;
  border: 1px dashed var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}
.recent-empty__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 64px;
  height: 64px;
  margin-bottom: var(--space-1);
  border-radius: var(--radius-xl);
  border: 1px solid color-mix(in srgb, var(--color-primary) 14%, transparent);
  background: linear-gradient(
    135deg,
    color-mix(in srgb, var(--color-primary) 14%, var(--color-surface)) 0%,
    var(--color-surface) 80%
  );
  color: var(--color-primary);
}
.recent-empty__title {
  font-size: var(--font-size-lg);
  font-weight: 600;
  color: var(--color-text-muted);
}
.recent-empty__desc {
  max-width: 340px;
  color: var(--color-text-faint);
  font-size: var(--font-size-sm);
  line-height: 1.55;
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
  color: var(--color-error);
  font-size: var(--font-size-sm);
}
@media (max-width: 1180px) {
  .recommend-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .ai-grid,
  .recent-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (max-width: 760px) {
  .recommend-grid,
  .ai-grid,
  .recent-grid {
    grid-template-columns: 1fr;
  }
  .wb-stats {
    flex-wrap: wrap;
    row-gap: 8px;
  }
  .wb-stat {
    padding: 0 16px;
  }
  .wb-stat:first-child {
    padding-left: 0;
  }
}
</style>
