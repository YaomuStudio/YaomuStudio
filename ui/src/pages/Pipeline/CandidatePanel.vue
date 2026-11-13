<script setup lang="ts">
import { computed } from "vue";
import { t, tCount } from "@/i18n";
import type { AudioCandidate } from "@/domain/types";
import type { EditorRow } from "./types";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmEmpty from "@/components/ui/YmEmpty.vue";
import YmIconButton from "@/components/ui/YmIconButton.vue";
import YmSpinner from "@/components/ui/YmSpinner.vue";

const props = defineProps<{
  rows: EditorRow[];
  selectedId: string | null;
  candidates: AudioCandidate[];
  loading: boolean;
  selectedCandidateId?: string;
  languageLabel: string;
  error: string;
  /** 当前正在试听的候选 ID（未播放时为空）；用于切换 play/pause 图标。 */
  playingCandidateId?: string;
}>();

const emit = defineEmits<{
  (e: "selectSegment", id: string): void;
  (e: "regenerate"): void;
  (e: "choose", candidateId: string): void;
  (e: "audition", candidateId: string): void;
}>();

const selectedRow = computed(
  () => props.rows.find((row) => row.translation.id === props.selectedId),
);
const hasCandidates = computed(() => props.candidates.length > 0);

function rowLabel(index: number): string {
  return String(index + 1).padStart(2, "0");
}
function onSelect(id: string): void {
  emit("selectSegment", id);
}
function onRegenerate(): void {
  emit("regenerate");
}
function onChoose(candidateId: string): void {
  emit("choose", candidateId);
}
function onAudition(candidateId: string): void {
  emit("audition", candidateId);
}
</script>

<template>
  <section class="candidates">
    <header class="candidates__head">
      <div class="candidates__titles">
        <h2 class="candidates__title">{{ t("pipeline.candidates.title") }}</h2>
        <p class="candidates__desc">{{ t("pipeline.candidates.desc") }}</p>
      </div>
      <div class="candidates__head-meta">
        <YmBadge tone="info">{{ languageLabel }}</YmBadge>
        <YmBadge v-if="hasCandidates" tone="neutral">
          <bdi>{{ tCount("pipeline.candidates.count", candidates.length) }}</bdi>
        </YmBadge>
      </div>
    </header>

    <div class="candidates__layout">
      <aside v-scroll-fade class="sentence-list ym-scroll-fade" :aria-label="t('pipeline.candidates.sentenceList')">
        <span class="sentence-list__label">{{ t("pipeline.candidates.sentenceList") }}</span>
        <button
          v-for="(row, index) in rows"
          :key="row.translation.id"
          type="button"
          class="sentence"
          :class="{ 'is-active': row.translation.id === selectedId }"
          :title="row.translation.text"
          @click="onSelect(row.translation.id)"
        >
          <span class="sentence__index mono"><bdi dir="ltr">{{ rowLabel(index) }}</bdi></span>
          <span class="sentence__text" dir="auto">{{ row.translation.text }}</span>
        </button>
      </aside>

      <div class="candidate-area">
        <YmEmpty
          v-if="!selectedRow"
          icon="mic"
          :title="t('pipeline.candidates.noSegmentTitle')"
          :description="t('pipeline.candidates.noSegmentDesc')"
        />

        <template v-else>
          <div class="sentence-preview">
            <div class="sentence-preview__col">
              <span class="sentence-preview__label">{{ t("pipeline.editor.original") }}</span>
              <p class="sentence-preview__text">{{ selectedRow.original?.text ?? "—" }}</p>
            </div>
            <div class="sentence-preview__col">
              <span class="sentence-preview__label">{{ t("pipeline.editor.translation") }}</span>
              <p class="sentence-preview__text">{{ selectedRow.translation.text }}</p>
            </div>
          </div>

          <!-- 参考句参数属于未决契约，只做「待定」说明，不编造字段。 -->
          <p class="pending-note">{{ t("pipeline.candidates.referencePending") }}</p>

          <div class="candidate-area__actions">
            <YmButton
              variant="secondary"
              size="sm"
              icon="refresh"
              :loading="loading"
              :disabled="loading"
              @click="onRegenerate"
            >
              {{ hasCandidates ? t("pipeline.candidates.regenerate") : t("pipeline.candidates.generate") }}
            </YmButton>
          </div>

          <div v-if="loading" class="candidate-loading">
            <YmSpinner :size="16" />
            <span>{{ t("pipeline.candidates.loading") }}</span>
          </div>

          <p v-else-if="error" class="candidate-error"><bdi>{{ error }}</bdi></p>

          <YmEmpty
            v-else-if="!hasCandidates"
            icon="volume"
            :title="t('pipeline.candidates.emptyTitle')"
            :description="t('pipeline.candidates.emptyDesc')"
          />

          <ul v-else class="candidate-list">
            <li
              v-for="(candidate, index) in candidates"
              :key="candidate.id"
              class="cand"
              :class="{ 'is-selected': selectedCandidateId === candidate.id }"
            >
              <div class="cand__head">
                <span class="cand__index mono">#{{ index + 1 }}</span>
                <YmBadge v-if="candidate.isRecommended" tone="accent">
                  {{ t("pipeline.candidates.recommended") }}
                </YmBadge>
                <YmBadge v-if="selectedCandidateId === candidate.id" tone="success">
                  {{ t("pipeline.candidates.selected") }}
                </YmBadge>
                <span class="cand__version">
                  {{ t("pipeline.candidates.version", { rev: candidate.revision, model: candidate.modelVersion }) }}
                </span>
              </div>
              <div class="cand__actions">
                <YmIconButton
                  :name="playingCandidateId === candidate.id ? 'pause' : 'play'"
                  :label="t(playingCandidateId === candidate.id ? 'pipeline.playback.pause' : 'pipeline.candidates.audition')"
                  :size="15"
                  @click="onAudition(candidate.id)"
                />
                <YmButton
                  variant="secondary"
                  size="sm"
                  :disabled="selectedCandidateId === candidate.id"
                  @click="onChoose(candidate.id)"
                >
                  {{ selectedCandidateId === candidate.id ? t("pipeline.candidates.selected") : t("pipeline.candidates.select") }}
                </YmButton>
              </div>
            </li>
          </ul>
        </template>
      </div>
    </div>
  </section>
</template>

<style scoped>
.candidates {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.candidates__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-3);
}
.candidates__titles {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.candidates__title {
  font-size: var(--font-size-lg);
}
.candidates__desc {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.candidates__head-meta {
  display: flex;
  gap: var(--space-2);
  flex: none;
}
.candidates__layout {
  display: grid;
  grid-template-columns: 220px minmax(0, 1fr);
  gap: var(--space-3);
  min-height: 320px;
}
.sentence-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 520px;
  overflow: auto;
  padding-inline-end: var(--space-1);
}
.sentence-list__label {
  font-size: var(--font-size-xs);
  font-weight: 500;
  color: var(--color-text-muted);
  padding: 0 var(--space-2) var(--space-1);
}
.sentence {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
  width: 100%;
  padding: var(--space-2);
  border-radius: var(--radius-sm);
  text-align: start;
  color: var(--color-text-muted);
}
.sentence:hover {
  background: var(--color-surface-2);
}
.sentence.is-active {
  background: var(--color-primary-soft);
  color: var(--color-text);
}
.sentence__index {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
  flex: none;
}
.sentence__text {
  font-size: var(--font-size-sm);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.candidate-area {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  min-width: 0;
}
.sentence-preview {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--space-3);
  padding: var(--space-3);
  background: var(--color-surface-2);
  border-radius: var(--radius-md);
}
.sentence-preview__col {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.sentence-preview__label {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.sentence-preview__text {
  font-size: var(--font-size-md);
  line-height: var(--line-height-normal);
  word-break: break-word;
}
.pending-note {
  align-self: flex-start;
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
  padding: 2px var(--space-2);
  border: 1px dashed var(--color-border-strong);
  border-radius: var(--radius-sm);
}
.candidate-area__actions {
  display: flex;
  justify-content: flex-end;
}
.candidate-loading {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-4);
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.candidate-error {
  font-size: var(--font-size-sm);
  color: var(--color-error-strong);
}
.candidate-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}
.cand {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-3);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.cand.is-selected {
  border-color: var(--color-success-strong);
  background: var(--color-success-soft);
}
.cand__head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
  min-width: 0;
}
.cand__index {
  font-size: var(--font-size-md);
  font-weight: 600;
}
.cand__version {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.cand__actions {
  display: flex;
  align-items: center;
  gap: var(--space-1);
  flex: none;
}
@media (max-width: 1200px) {
  .candidates__layout {
    grid-template-columns: minmax(0, 1fr);
  }
  .sentence-list {
    max-height: 180px;
  }
}
</style>
