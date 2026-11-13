<script setup lang="ts">
import { computed, nextTick, ref } from "vue";
import { t, tCount } from "@/i18n";
import type { EditorRow, SegmentActionState, TimeDraft } from "./types";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmEmpty from "@/components/ui/YmEmpty.vue";
import YmIconButton from "@/components/ui/YmIconButton.vue";
import YmTextArea from "@/components/ui/YmTextArea.vue";
import YmTextInput from "@/components/ui/YmTextInput.vue";

const props = withDefaults(
  defineProps<{
    rows: EditorRow[];
    drafts: Record<string, TimeDraft>;
    errors: Record<string, string[]>;
    selectedId: string | null;
    dirty: boolean;
    saving: boolean;
    hasTrack: boolean;
    languageLabel: string;
    durationLabel: string;
    /** 当前正在试听的句段 ID（未播放时为空）；用于切换 play/pause 图标。 */
    playingId?: string;
    /** 逐句试听可用性：无源素材或当前草稿时间非法时为 false（按钮禁用）。 */
    auditionable?: Record<string, boolean>;
    /** 合并/拆分禁用原因：最后一行、时间窗不足、草稿非法或无译文轨。 */
    actions?: Record<string, SegmentActionState>;
    /**
     * 是否在编辑器头部显示保存按钮与 dirty 徽标。
     * 宿主页面已有唯一保存入口时显式关闭；默认保持原行为，不影响 Pipeline 消费者。
     */
    showSaveControls?: boolean;
  }>(),
  { showSaveControls: true },
);

const emit = defineEmits<{
  (e: "update:text", id: string, value: string): void;
  (e: "update:time", id: string, field: "start" | "end", value: string): void;
  (e: "select", id: string): void;
  (e: "audition", id: string): void;
  (e: "merge", id: string): void;
  (e: "split", id: string): void;
  (e: "save"): void;
}>();

const hasErrors = computed(() =>
  Object.values(props.errors).some((messages) => messages.length > 0),
);
const canSave = computed(() => props.dirty && !props.saving && !hasErrors.value);
const hasSelectedRow = computed(() =>
  props.rows.some((row) => row.translation.id === props.selectedId),
);

const rowsEl = ref<HTMLElement | null>(null);

function rowElement(id: string | null): HTMLElement | undefined {
  if (!id) return undefined;
  const elements = rowsEl.value?.querySelectorAll<HTMLElement>("[data-segment-id]");
  return Array.from(elements ?? []).find((element) => element.dataset.segmentId === id);
}

function focusRow(id: string | null): void {
  if (!id) return;
  void nextTick(() => rowElement(id)?.focus());
}

/** 行级 roving tabindex：选中行是唯一 Tab 停靠点；无选中时首行兜底。 */
function rowTabIndex(id: string, index: number): number {
  if (props.selectedId === id) return 0;
  return !hasSelectedRow.value && index === 0 ? 0 : -1;
}

/**
 * 行内静态文案跟随当前语言与语言包；对象作为 v-memo 的单一语言依赖：
 * 切换语言或语言包异步加载完成后重建，所有行随之刷新一次。
 */
const rowLabels = computed(() => ({
  original: t("pipeline.editor.original"),
  translation: t("pipeline.editor.translation"),
  timestamp: t("pipeline.editor.timestamp"),
  start: t("pipeline.editor.start"),
  end: t("pipeline.editor.end"),
  merge: t("pipeline.editor.merge"),
  split: t("pipeline.editor.split"),
  audition: t("pipeline.editor.audition"),
  pause: t("pipeline.playback.pause"),
}));

/**
 * 行级渲染隔离：v-memo 依赖必须覆盖该行全部可见输入，使文本/时间草稿/错误/
 * 选中/试听/合并拆分状态变化只重渲染受影响行。行 key 仍为翻译句段 ID，
 * DOM 常驻，焦点、IME、光标、选择与 roving tabindex 语义不变。
 */
function rowMemo(row: EditorRow, index: number): unknown[] {
  const id = row.translation.id;
  return [
    id,
    index,
    props.selectedId === id,
    props.playingId === id,
    props.errors[id],
    props.drafts[id]?.start,
    props.drafts[id]?.end,
    props.auditionable?.[id],
    props.actions?.[id]?.mergeDisabledReason,
    props.actions?.[id]?.splitDisabledReason,
    row.translation.text,
    row.original?.text,
    row.original?.speaker,
    rowTabIndex(id, index),
    rowLabels.value,
  ];
}

function selectRow(id: string): void {
  if (props.selectedId !== id) emit("select", id);
}

/**
 * 只在行容器自身聚焦时处理方向键；行内 textarea/按钮与 IME composition
 * 的事件目标不是容器，保留原生键盘语义。
 */
function onRowKeydown(event: KeyboardEvent, index: number): void {
  if (event.target !== event.currentTarget || event.isComposing) return;
  if (event.ctrlKey || event.metaKey || event.altKey) return;
  let nextIndex: number;
  switch (event.key) {
    case "ArrowDown":
      nextIndex = Math.min(index + 1, props.rows.length - 1);
      break;
    case "ArrowUp":
      nextIndex = Math.max(index - 1, 0);
      break;
    case "Home":
      nextIndex = 0;
      break;
    case "End":
      nextIndex = props.rows.length - 1;
      break;
    default:
      return;
  }
  const target = props.rows[nextIndex];
  if (!target) return;
  event.preventDefault();
  selectRow(target.translation.id);
  focusRow(target.translation.id);
}

function onSelect(id: string): void {
  emit("select", id);
}
function onAudition(id: string): void {
  emit("audition", id);
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
function onSave(): void {
  emit("save");
}
</script>

<template>
  <section class="editor">
    <header class="editor__head">
      <div class="editor__titles">
        <h2 class="editor__title">{{ t("pipeline.editor.title") }}</h2>
        <p class="editor__desc">{{ t("pipeline.editor.desc") }}</p>
        <div class="editor__meta">
          <YmBadge tone="info">{{ languageLabel }}</YmBadge>
          <span class="editor__meta-item">
            {{ t("pipeline.editor.duration") }}: <bdi dir="ltr">{{ durationLabel }}</bdi>
          </span>
          <span class="editor__meta-item">
            <bdi>{{ tCount("pipeline.editor.segmentCount", rows.length) }}</bdi>
          </span>
        </div>
      </div>
      <div v-if="showSaveControls" class="editor__actions">
        <YmBadge v-if="dirty" tone="warning">{{ t("pipeline.editor.dirty") }}</YmBadge>
        <YmButton
          variant="primary"
          size="sm"
          icon="check"
          :disabled="!canSave"
          :loading="saving"
          @click="onSave"
        >
          {{ t("pipeline.editor.save") }}
        </YmButton>
      </div>
    </header>

    <p v-if="hasErrors" class="editor__blocked">{{ t("pipeline.editor.validationBlocked") }}</p>
    <p v-else class="editor__hint">{{ t("pipeline.editor.selectHint") }}</p>

    <YmEmpty
      v-if="!hasTrack"
      icon="subtitles"
      :title="t('pipeline.editor.noTranslationTitle')"
      :description="t('pipeline.editor.noTranslationDesc')"
    />
    <YmEmpty
      v-else-if="rows.length === 0"
      icon="subtitles"
      :title="t('pipeline.editor.noTranslationTitle')"
      :description="t('pipeline.editor.noTranslationDesc')"
    />

    <div v-else ref="rowsEl" class="editor__rows">
      <article
        v-for="(row, index) in rows"
        :key="row.translation.id"
        v-memo="rowMemo(row, index)"
        class="seg"
        :class="{
          'is-selected': selectedId === row.translation.id,
          'has-error': (errors[row.translation.id]?.length ?? 0) > 0,
        }"
        data-testid="segment-row"
        :data-segment-id="row.translation.id"
        :tabindex="rowTabIndex(row.translation.id, index)"
        :aria-current="selectedId === row.translation.id ? 'true' : undefined"
        :aria-label="t('pipeline.editor.rowLabel', { index: index + 1 })"
        @click="onSelect(row.translation.id)"
        @keydown="onRowKeydown($event, index)"
      >
        <div class="seg__bar">
          <span class="seg__index mono"><bdi dir="ltr">{{ String(index + 1).padStart(2, "0") }}</bdi></span>
          <span v-if="row.original?.speaker" class="seg__speaker"><bdi dir="auto">{{ row.original.speaker }}</bdi></span>
          <div class="seg__bar-actions">
            <YmIconButton
              :name="playingId === row.translation.id ? 'pause' : 'play'"
              :label="playingId === row.translation.id ? rowLabels.pause : rowLabels.audition"
              :size="15"
              :disabled="auditionable?.[row.translation.id] === false"
              @click.stop="onAudition(row.translation.id)"
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
          </div>
        </div>

        <div class="seg__cols">
          <div class="seg__col">
            <span class="seg__label">{{ rowLabels.original }}</span>
            <p class="seg__original" dir="auto">{{ row.original?.text ?? "—" }}</p>
          </div>
          <div class="seg__col">
            <span class="seg__label">{{ rowLabels.translation }}</span>
            <YmTextArea
              :model-value="row.translation.text"
              :rows="2"
              dir="auto"
              :aria-label="`${rowLabels.translation} ${index + 1}`"
              @update:model-value="emit('update:text', row.translation.id, $event)"
            />
          </div>
          <div class="seg__col seg__col--time">
            <span class="seg__label">{{ rowLabels.timestamp }}</span>
            <label class="time-field">
              <span>{{ rowLabels.start }}</span>
              <YmTextInput
                class="mono"
                dir="ltr"
                :model-value="drafts[row.translation.id]?.start ?? ''"
                @update:model-value="emit('update:time', row.translation.id, 'start', $event)"
              />
            </label>
            <label class="time-field">
              <span>{{ rowLabels.end }}</span>
              <YmTextInput
                class="mono"
                dir="ltr"
                :model-value="drafts[row.translation.id]?.end ?? ''"
                @update:model-value="emit('update:time', row.translation.id, 'end', $event)"
              />
            </label>
          </div>
        </div>

        <ul v-if="(errors[row.translation.id]?.length ?? 0) > 0" class="seg__errors">
          <li v-for="message in errors[row.translation.id] ?? []" :key="message">{{ message }}</li>
        </ul>
      </article>
    </div>
  </section>
</template>

<style scoped>
.editor {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.editor__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-3);
}
.editor__titles {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.editor__title {
  font-size: var(--font-size-lg);
}
.editor__desc {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.editor__meta {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
  margin-top: 2px;
}
.editor__meta-item {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.editor__actions {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex: none;
}
.editor__hint {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.editor__blocked {
  font-size: var(--font-size-xs);
  color: var(--color-error-strong);
}
.editor__rows {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.seg {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-3);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: border-color var(--motion-duration-snap) var(--motion-ease-snap);
}
.seg:hover {
  border-color: var(--color-border-strong);
}
.seg.is-selected {
  border-color: var(--color-primary);
  box-shadow: 0 0 0 2px var(--color-primary-soft);
}
.seg:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: -2px;
}
.seg.has-error {
  border-color: var(--color-error-strong);
}
.seg__bar {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.seg__index {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.seg__speaker {
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
}
.seg__bar-actions {
  margin-inline-start: auto;
  display: flex;
  align-items: center;
  gap: var(--space-1);
}
.seg__cols {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1.2fr) 148px;
  gap: var(--space-3);
}
.seg__col {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  min-width: 0;
}
.seg__label {
  font-size: var(--font-size-xs);
  font-weight: 500;
  color: var(--color-text-muted);
}
.seg__original {
  font-size: var(--font-size-md);
  line-height: var(--line-height-normal);
  color: var(--color-text-muted);
  padding: 8px 0;
  word-break: break-word;
}
.time-field {
  display: flex;
  align-items: center;
  gap: var(--space-1);
}
.time-field span {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
  width: 28px;
  flex: none;
}
.seg__errors {
  list-style: none;
  margin: 0;
  padding: var(--space-1) var(--space-2);
  display: flex;
  flex-direction: column;
  gap: 2px;
  background: var(--color-error-soft);
  border-radius: var(--radius-sm);
  color: var(--color-error-strong);
  font-size: var(--font-size-xs);
}
@media (max-width: 1200px) {
  .seg__cols {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
