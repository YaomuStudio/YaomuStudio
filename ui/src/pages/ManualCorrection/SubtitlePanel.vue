<script setup lang="ts">
// 内容校正面板：字幕卡片（时间码 / 原文 / 译文 / 行内配音候选）。
//
// 本组件只做展示与事件转发：编辑、合并/拆分、键盘 roving、候选生成/选择/试听
// 全部由页面委托给既有 Core handlers；不保存本地候选、不模拟生成或播放。
import { computed, nextTick, ref } from "vue";
import { t, tCount } from "@/i18n";
import type { AudioCandidate } from "@/domain/types";
import type { EditorRow, SegmentActionState, TimeDraft } from "@/pages/Pipeline/types";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmIconButton from "@/components/ui/YmIconButton.vue";
import YmSelect from "@/components/ui/YmSelect.vue";
import YmSpinner from "@/components/ui/YmSpinner.vue";
import YmTextArea from "@/components/ui/YmTextArea.vue";
import YmTextInput from "@/components/ui/YmTextInput.vue";

interface LanguageOption {
  value: string;
  label: string;
}

const props = defineProps<{
  rows: EditorRow[];
  drafts: Record<string, TimeDraft>;
  errors: Record<string, string[]>;
  selectedId: string | null;
  languageLabel: string;
  languages: LanguageOption[];
  language: string;
  /** 有源轨时允许编辑原文；无源轨保持只读空态。 */
  sourceEditable?: boolean;
  playingId?: string;
  auditionable?: Record<string, boolean>;
  actions?: Record<string, SegmentActionState>;
  /** 行内候选展开的句段；同一时刻只展开一行。 */
  expandedId: string | null;
  candidates: AudioCandidate[];
  candidateLoading: boolean;
  candidateError: string;
  selectedCandidateId?: string;
  playingCandidateId?: string;
}>();

const emit = defineEmits<{
  (e: "select", id: string): void;
  (e: "update:text", id: string, value: string): void;
  (e: "update:source-text", id: string, value: string): void;
  (e: "update:time", id: string, field: "start" | "end", value: string): void;
  (e: "audition", id: string): void;
  (e: "merge", id: string): void;
  (e: "split", id: string): void;
  (e: "toggleCandidates", id: string): void;
  (e: "selectLanguage", language: string): void;
  (e: "generateCandidates"): void;
  (e: "choose", candidateId: string): void;
  (e: "auditionCandidate", candidateId: string): void;
}>();

const query = ref("");
const listEl = ref<HTMLElement | null>(null);

const filteredRows = computed(() => {
  const keyword = query.value.trim().toLowerCase();
  if (!keyword) return props.rows;
  return props.rows.filter((row) =>
    `${row.original?.text ?? ""} ${row.translation.text}`.toLowerCase().includes(keyword),
  );
});

const hasSelectedRow = computed(() =>
  filteredRows.value.some((row) => row.translation.id === props.selectedId),
);
const hasCandidates = computed(() => props.candidates.length > 0);

/**
 * 行内静态文案跟随当前语言与语言包；对象作为 v-memo 的单一语言依赖：
 * 切换语言或语言包异步加载完成后重建，所有行随之刷新一次。
 */
const rowLabels = computed(() => ({
  original: t("pipeline.editor.original"),
  translation: t("pipeline.editor.translation"),
  start: t("pipeline.editor.start"),
  end: t("pipeline.editor.end"),
  audition: t("pipeline.editor.audition"),
  pause: t("pipeline.playback.pause"),
  merge: t("pipeline.editor.merge"),
  split: t("pipeline.editor.split"),
  candidates: t("pipeline.candidates.title"),
  generate: t("pipeline.candidates.generate"),
  regenerate: t("pipeline.candidates.regenerate"),
  loading: t("mcs.candidates.loading"),
  empty: t("pipeline.candidates.emptyDesc"),
  recommended: t("pipeline.candidates.recommended"),
  selected: t("pipeline.candidates.selected"),
  select: t("pipeline.candidates.select"),
  auditionCandidate: t("pipeline.candidates.audition"),
  version: t("pipeline.candidates.version", { rev: 0, model: "" }),
}));

/** 行级 roving tabindex：选中行是唯一 Tab 停靠点；无选中时首行兜底。 */
function rowTabIndex(id: string, index: number): number {
  if (props.selectedId === id) return 0;
  return !hasSelectedRow.value && index === 0 ? 0 : -1;
}

function rowElement(id: string | null): HTMLElement | undefined {
  if (!id) return undefined;
  const elements = listEl.value?.querySelectorAll<HTMLElement>("[data-segment-id]");
  return Array.from(elements ?? []).find((element) => element.dataset.segmentId === id);
}

function focusRow(id: string | null): void {
  if (!id) return;
  void nextTick(() => rowElement(id)?.focus());
}

function selectRow(id: string): void {
  if (props.selectedId !== id) emit("select", id);
}

/** 只在行容器自身聚焦时处理方向键；行内 textarea/按钮与 IME 保留原生语义。 */
function onRowKeydown(event: KeyboardEvent, index: number): void {
  if (event.target !== event.currentTarget || event.isComposing) return;
  if (event.ctrlKey || event.metaKey || event.altKey) return;
  let nextIndex: number;
  switch (event.key) {
    case "ArrowDown":
      nextIndex = Math.min(index + 1, filteredRows.value.length - 1);
      break;
    case "ArrowUp":
      nextIndex = Math.max(index - 1, 0);
      break;
    case "Home":
      nextIndex = 0;
      break;
    case "End":
      nextIndex = filteredRows.value.length - 1;
      break;
    default:
      return;
  }
  const target = filteredRows.value[nextIndex];
  if (!target) return;
  event.preventDefault();
  selectRow(target.translation.id);
  focusRow(target.translation.id);
}

function focusSelectedRowAfterAction(action: () => void): void {
  const previous = props.selectedId;
  action();
  // 合并/拆分替换并移除原行（含触发按钮）；父级更新选中行后把焦点交还新行。
  void nextTick(() => {
    if (props.selectedId && props.selectedId !== previous) {
      rowElement(props.selectedId)?.focus();
    }
  });
}

function onMerge(id: string): void {
  focusSelectedRowAfterAction(() => emit("merge", id));
}
function onSplit(id: string): void {
  focusSelectedRowAfterAction(() => emit("split", id));
}
function onToggleCandidates(id: string): void {
  emit("select", id);
  emit("toggleCandidates", id);
}
function segmentNumber(id: string): number {
  return props.rows.findIndex((row) => row.translation.id === id) + 1;
}
function selectLabel(candidateId: string): string {
  return props.selectedCandidateId === candidateId
    ? t("pipeline.candidates.selected")
    : t("pipeline.candidates.select");
}

/**
 * 行级渲染隔离：v-memo 依赖覆盖该行全部可见输入，使文本/时间草稿/错误/
 * 选中/试听/合并拆分/候选状态变化只重渲染受影响行；DOM 常驻，焦点、IME、
 * 光标、选择与 roving tabindex 语义不变。
 */
function rowMemo(row: EditorRow, index: number): unknown[] {
  const id = row.translation.id;
  const deps: unknown[] = [
    id,
    index,
    props.selectedId === id,
    props.expandedId === id,
    props.playingId === id,
    props.errors[id],
    props.drafts[id]?.start,
    props.drafts[id]?.end,
    row.translation.startMs,
    row.translation.endMs,
    row.translation.text,
    row.original?.text,
    row.original?.speaker,
    props.auditionable?.[id],
    props.actions?.[id]?.mergeDisabledReason,
    props.actions?.[id]?.splitDisabledReason,
    rowTabIndex(id, index),
    rowLabels.value,
  ];
  if (props.expandedId === id) {
    deps.push(
      props.candidates,
      props.candidateLoading,
      props.candidateError,
      props.selectedCandidateId,
      props.playingCandidateId,
      props.languageLabel,
      hasCandidates.value,
    );
  }
  return deps;
}
</script>

<template>
  <section class="subpanel">
    <header class="subpanel__head">
      <h2 class="subpanel__title">{{ t("mcs.tab.subtitle") }}</h2>
      <span class="subpanel__count">
        <bdi>{{ tCount("pipeline.editor.segmentCount", rows.length) }}</bdi>
      </span>
      <YmBadge tone="info">{{ languageLabel }}</YmBadge>
    </header>

    <div class="subpanel__tools">
      <label v-if="languages.length > 1" class="subpanel__lang">
        <span>{{ t("pipeline.editor.language") }}</span>
        <YmSelect
          class="subpanel__select"
          :model-value="language"
          :options="languages"
          :aria-label="t('pipeline.editor.language')"
          @update:model-value="emit('selectLanguage', $event)"
        />
      </label>
      <div class="subpanel__search">
        <YmIcon name="search" :size="13" />
        <input
          v-model="query"
          type="search"
          dir="auto"
          :aria-label="t('mcs.sub.search')"
          :placeholder="t('mcs.sub.search')"
        />
      </div>
    </div>

    <p v-if="rows.length > 0 && filteredRows.length === 0" class="subpanel__empty">
      {{ t("mcs.sub.empty") }}
    </p>

    <div v-else ref="listEl" class="sub-list">
      <article
        v-for="(row, index) in filteredRows"
        :key="row.translation.id"
        v-memo="rowMemo(row, index)"
        class="sub"
        :class="{
          'is-selected': selectedId === row.translation.id,
          'is-expanded': expandedId === row.translation.id,
          'has-error': (errors[row.translation.id]?.length ?? 0) > 0,
        }"
        data-testid="segment-row"
        :data-segment-id="row.translation.id"
        :tabindex="rowTabIndex(row.translation.id, index)"
        :aria-current="selectedId === row.translation.id ? 'true' : undefined"
        :aria-label="t('pipeline.editor.rowLabel', { index: segmentNumber(row.translation.id) })"
        @click="selectRow(row.translation.id)"
        @keydown="onRowKeydown($event, index)"
      >
        <div class="sub__top">
          <span class="sub__index mono">
            <bdi dir="ltr">{{ String(segmentNumber(row.translation.id)).padStart(2, "0") }}</bdi>
          </span>
          <span v-if="row.original?.speaker" class="sub__speaker">
            <bdi dir="auto">{{ row.original.speaker }}</bdi>
          </span>
          <div class="sub__actions">
            <YmIconButton
              :name="playingId === row.translation.id ? 'pause' : 'play'"
              :label="
                playingId === row.translation.id ? rowLabels.pause : rowLabels.audition
              "
              :size="15"
              :disabled="auditionable?.[row.translation.id] === false"
              @click.stop="emit('audition', row.translation.id)"
            />
            <YmButton
              variant="ghost"
              size="sm"
              data-testid="segment-merge"
              :disabled="Boolean(actions?.[row.translation.id]?.mergeDisabledReason)"
              :title="actions?.[row.translation.id]?.mergeDisabledReason"
              @click.stop="onMerge(row.translation.id)"
            >
              {{ rowLabels.merge }}
            </YmButton>
            <YmButton
              variant="ghost"
              size="sm"
              data-testid="segment-split"
              :disabled="Boolean(actions?.[row.translation.id]?.splitDisabledReason)"
              :title="actions?.[row.translation.id]?.splitDisabledReason"
              @click.stop="onSplit(row.translation.id)"
            >
              {{ rowLabels.split }}
            </YmButton>
            <button
              type="button"
              class="sub__expand"
              :aria-expanded="expandedId === row.translation.id"
              data-testid="candidate-toggle"
              @click.stop="onToggleCandidates(row.translation.id)"
            >
              <YmIcon
                :name="expandedId === row.translation.id ? 'chevron-up' : 'chevron-down'"
                :size="12"
              />
              <span>{{ rowLabels.candidates }}</span>
            </button>
          </div>
        </div>

        <div class="sub__time">
          <label class="time-field">
            <span>{{ rowLabels.start }}</span>
            <YmTextInput
              class="mono sub__time-input"
              dir="ltr"
              :model-value="drafts[row.translation.id]?.start ?? ''"
              @update:model-value="emit('update:time', row.translation.id, 'start', $event)"
            />
          </label>
          <span class="sub__time-sep" aria-hidden="true">→</span>
          <label class="time-field">
            <span>{{ rowLabels.end }}</span>
            <YmTextInput
              class="mono sub__time-input"
              dir="ltr"
              :model-value="drafts[row.translation.id]?.end ?? ''"
              @update:model-value="emit('update:time', row.translation.id, 'end', $event)"
            />
          </label>
        </div>

        <div class="sub__cols">
          <div class="sub__col">
            <span class="sub__label">{{ rowLabels.original }}</span>
            <YmTextArea
              v-if="sourceEditable && row.original"
              class="sub__original-input"
              data-testid="segment-source"
              :model-value="row.original.text"
              :rows="2"
              dir="auto"
              :aria-label="`${rowLabels.original} ${segmentNumber(row.translation.id)}`"
              @update:model-value="emit('update:source-text', row.original.id, $event)"
            />
            <p v-else class="sub__original" dir="auto">{{ row.original?.text ?? "—" }}</p>
          </div>
          <div class="sub__col">
            <span class="sub__label">{{ rowLabels.translation }}</span>
            <YmTextArea
              data-testid="segment-translation"
              :model-value="row.translation.text"
              :rows="2"
              dir="auto"
              :aria-label="`${rowLabels.translation} ${segmentNumber(row.translation.id)}`"
              @update:model-value="emit('update:text', row.translation.id, $event)"
            />
          </div>
        </div>

        <ul v-if="(errors[row.translation.id]?.length ?? 0) > 0" class="sub__errors">
          <li v-for="message in errors[row.translation.id] ?? []" :key="message">{{ message }}</li>
        </ul>

        <div
          v-if="expandedId === row.translation.id"
          class="sub__dub"
          data-testid="inline-candidates"
          @click.stop
        >
          <div class="sub__dub-head">
            <span class="sub__dub-title">{{ rowLabels.candidates }}</span>
            <YmBadge tone="info">{{ languageLabel }}</YmBadge>
            <YmButton
              variant="secondary"
              size="sm"
              icon="refresh"
              :loading="candidateLoading"
              :disabled="candidateLoading"
              @click="emit('generateCandidates')"
            >
              {{ hasCandidates ? rowLabels.regenerate : rowLabels.generate }}
            </YmButton>
          </div>

          <div v-if="candidateLoading" class="sub__dub-state">
            <YmSpinner :size="16" />
            <span>{{ rowLabels.loading }}</span>
          </div>
          <p v-else-if="candidateError" class="sub__dub-error" role="alert">
            <bdi>{{ candidateError }}</bdi>
          </p>
          <p v-else-if="!hasCandidates" class="sub__dub-state">
            {{ rowLabels.empty }}
          </p>

          <ul v-else class="candidate-list">
            <li
              v-for="(candidate, candidateIndex) in candidates"
              :key="candidate.id"
              class="cand"
              :class="{ 'is-selected': selectedCandidateId === candidate.id }"
            >
              <span class="cand__index mono">#{{ candidateIndex + 1 }}</span>
              <span class="cand__version">
                {{
                  t("pipeline.candidates.version", {
                    rev: candidate.revision,
                    model: candidate.modelVersion,
                  })
                }}
              </span>
              <YmBadge v-if="candidate.isRecommended" tone="accent">
                {{ rowLabels.recommended }}
              </YmBadge>
              <YmBadge v-if="selectedCandidateId === candidate.id" tone="success">
                {{ rowLabels.selected }}
              </YmBadge>
              <span class="cand__ops">
                <YmIconButton
                  :name="playingCandidateId === candidate.id ? 'pause' : 'play'"
                  :label="
                    playingCandidateId === candidate.id
                      ? rowLabels.pause
                      : rowLabels.auditionCandidate
                  "
                  :size="15"
                  @click="emit('auditionCandidate', candidate.id)"
                />
                <YmButton
                  variant="secondary"
                  size="sm"
                  :disabled="selectedCandidateId === candidate.id"
                  @click="emit('choose', candidate.id)"
                >
                  {{ selectLabel(candidate.id) }}
                </YmButton>
              </span>
            </li>
          </ul>
        </div>
      </article>
    </div>
  </section>
</template>

<style scoped>
.subpanel {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  min-height: 0;
}
.subpanel__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  flex: none;
}
.subpanel__title {
  font-size: var(--font-size-lg);
}
.subpanel__count {
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
  margin-inline-start: auto;
}
.subpanel__tools {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex: none;
}
.subpanel__lang {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex: none;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.subpanel__select {
  width: 140px;
}
.subpanel__search {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  height: 30px;
  padding: 0 10px;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-sm);
  color: var(--color-text-faint);
}
.subpanel__search:focus-within {
  border-color: var(--color-primary);
}
.subpanel__search input {
  flex: 1;
  min-width: 0;
  height: 100%;
  border: none;
  outline: none;
  background: transparent;
  color: var(--color-text);
  font-size: var(--font-size-sm);
}
.subpanel__search input:focus-visible {
  /* 焦点由外层边框统一表达，避免内层再叠一层焦点环。 */
  box-shadow: none;
}
.subpanel__empty {
  margin: 0;
  padding: var(--space-6) 0;
  color: var(--color-text-faint);
  font-size: var(--font-size-sm);
  text-align: center;
}
.sub-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.sub {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: var(--color-surface);
  cursor: pointer;
}
.sub:hover {
  border-color: var(--color-border-strong);
}
.sub.is-selected {
  border-color: var(--color-primary);
  box-shadow: 0 0 0 2px var(--color-primary-soft);
}
.sub.is-expanded {
  border-color: var(--color-primary);
}
.sub.has-error {
  border-color: var(--color-error-strong);
}
.sub:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: -2px;
}
.sub__top {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.sub__index {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 20px;
  height: 20px;
  flex: none;
  border-radius: var(--radius-pill);
  border: 1px solid var(--color-border-strong);
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
}
.sub__speaker {
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
}
.sub__actions {
  margin-inline-start: auto;
  display: flex;
  align-items: center;
  gap: var(--space-1);
  flex-wrap: wrap;
}
.sub__expand {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  color: var(--color-primary-ink);
  font-size: var(--font-size-xs);
}
.sub__expand:hover {
  border-color: var(--color-primary);
  background: var(--color-primary-soft);
}
.sub__time {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.time-field {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
}
.time-field span {
  flex: none;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.sub__time-input {
  width: 132px;
}
.sub__time-sep {
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.sub__cols {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1.15fr);
  gap: var(--space-3);
}
.sub__col {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  min-width: 0;
}
.sub__label {
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
  font-weight: 500;
}
.sub__original {
  margin: 0;
  padding: 8px 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-md);
  line-height: var(--line-height-normal);
  word-break: break-word;
}
.sub__errors {
  list-style: none;
  margin: 0;
  padding: var(--space-1) var(--space-2);
  display: flex;
  flex-direction: column;
  gap: 2px;
  border-radius: var(--radius-sm);
  background: var(--color-error-soft);
  color: var(--color-error-strong);
  font-size: var(--font-size-xs);
}
.sub__dub {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin-top: var(--space-1);
  padding: var(--space-3);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
}
.sub__dub-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.sub__dub-title {
  color: var(--color-text);
  font-size: var(--font-size-sm);
  font-weight: 600;
}
.sub__dub-head :deep(.ym-btn) {
  margin-inline-start: auto;
}
.sub__dub-state {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
}
.sub__dub-error {
  margin: 0;
  color: var(--color-error-strong);
  font-size: var(--font-size-xs);
}
.candidate-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  margin: 0;
  padding: 0;
  list-style: none;
}
.cand {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
  padding: var(--space-2);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}
.cand.is-selected {
  border-color: var(--color-success-strong);
  background: var(--color-success-soft);
}
.cand__index {
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.cand__version {
  flex: 1;
  min-width: 120px;
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
}
.cand__ops {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  margin-inline-start: auto;
}
@media (max-width: 1200px) {
  .sub__cols {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
