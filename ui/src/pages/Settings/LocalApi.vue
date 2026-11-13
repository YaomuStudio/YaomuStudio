<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { t } from "@/i18n";
import { errorNotice, messageNotice, renderNotice, type SafeNotice } from "@/i18n/notice";
import { api } from "@/api";
import type { LocalApiSettings, LocalApiStats } from "@/domain/types";
import PageHeader from "@/components/layout/PageHeader.vue";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmDialog from "@/components/ui/YmDialog.vue";
import YmField from "@/components/ui/YmField.vue";
import YmNotice from "@/components/ui/YmNotice.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmSelect from "@/components/ui/YmSelect.vue";
import YmSwitch from "@/components/ui/YmSwitch.vue";

// 总开关、并发上限与令牌都由 Core 的 /local-api/settings 与 /local-api/token 管理；
// UI 不本地生成令牌，明文令牌只在签发响应出现一次，仅保存在内存里。
const settings = ref<LocalApiSettings | null>(null);
const stats = ref<LocalApiStats | null>(null);
const loading = ref(false);
const error = ref<SafeNotice | null>(null);
const errorText = computed(() => renderNotice(error.value));
const notice = ref<SafeNotice | null>(null);
const noticeText = computed(() => renderNotice(notice.value));
const token = ref<string | null>(null);
const enableDialog = ref(false);
const revokeDialog = ref(false);
const concurrency = ref("2");

const enabled = computed(() => settings.value?.enabled ?? false);

const concurrencyOptions = computed(() => [
  { value: "1", label: "1" },
  { value: "2", label: "2" },
  { value: "4", label: "4" },
  { value: "8", label: "8" },
  { value: "0", label: t("settings.localApi.unlimited") },
]);

const tokenDisplay = computed(
  () => token.value ?? settings.value?.tokenMasked ?? t("settings.localApi.tokenPlaceholder"),
);


async function run(action: () => Promise<void>): Promise<void> {
  error.value = null;
  try {
    await action();
  } catch (err) {
    error.value = errorNotice(err, "settings.localApi.error");
  }
}

async function load(): Promise<void> {
  loading.value = true;
  error.value = null;
  try {
    const [nextSettings, nextStats] = await Promise.all([
      api.getLocalApiSettings(),
      api.getLocalApiStats(),
    ]);
    settings.value = nextSettings;
    stats.value = nextStats;
    concurrency.value = String(nextSettings.concurrencyLimit);
  } catch (err) {
    error.value = errorNotice(err, "settings.localApi.error");
  } finally {
    loading.value = false;
  }
}

function onLocalApi(value: boolean): void {
  if (value) {
    enableDialog.value = true;
  } else {
    void disableApi();
  }
}

async function confirmEnable(): Promise<void> {
  enableDialog.value = false;
  await run(async () => {
    const next = await api.updateLocalApiSettings({ enabled: true });
    settings.value = next;
    if (!next.tokenPresent) {
      const issued = await api.issueLocalApiToken();
      token.value = issued.token;
    }
    await load();
    notice.value = messageNotice("settings.localApi.enabled");
  });
}

async function disableApi(): Promise<void> {
  await run(async () => {
    settings.value = await api.updateLocalApiSettings({ enabled: false });
    token.value = null;
    await load();
  });
}

async function rotateToken(): Promise<void> {
  if (!enabled.value) return;
  await run(async () => {
    const issued = await api.issueLocalApiToken();
    token.value = issued.token;
    await load();
    notice.value = messageNotice("settings.localApi.rotated");
  });
}

async function confirmRevoke(): Promise<void> {
  revokeDialog.value = false;
  await run(async () => {
    await api.revokeLocalApiToken();
    settings.value = await api.updateLocalApiSettings({ enabled: false });
    token.value = null;
    await load();
    notice.value = messageNotice("settings.localApi.revoked");
  });
}

async function onConcurrency(value: string): Promise<void> {
  concurrency.value = value;
  await run(async () => {
    settings.value = await api.updateLocalApiSettings({ concurrencyLimit: Number(value) });
    notice.value = messageNotice("settings.localApi.concurrencySaved");
  });
}

onMounted(() => {
  void load();
});
</script>

<template>
  <div class="page">
    <PageHeader
      :title="t('settings.localApi.title')"
      :description="t('settings.localApi.desc')"
      icon="link"
    />

    <YmCard>
      <div class="stack">
        <div class="switch-row">
          <div class="switch-row__text">
            <p class="switch-row__label">{{ t("settings.localApi.enable") }}</p>
            <p class="switch-row__hint">{{ t("settings.localApi.enableHint") }}</p>
          </div>
          <YmSwitch
            :aria-label="t('settings.localApi.enable')"
            :model-value="enabled"
            @update:model-value="onLocalApi"
          />
        </div>

        <div class="security">
          <YmIcon name="shield" :size="16" />
          <div>
            <p class="security__title">{{ t("settings.localApi.binding") }}</p>
            <p class="security__value mono">{{ t("settings.localApi.bindingValue") }}</p>
          </div>
        </div>
      </div>
    </YmCard>

    <YmCard>
      <div class="token">
        <div class="token__head">
          <div>
            <h2 class="section-title">{{ t("settings.localApi.token") }}</h2>
            <p class="hint">{{ t("settings.localApi.tokenHint") }}</p>
          </div>
          <YmBadge :tone="enabled ? 'accent' : 'neutral'">
            {{ enabled ? t("state.ready") : t("state.disabled") }}
          </YmBadge>
        </div>

        <p class="token__value mono">{{ tokenDisplay }}</p>

        <div class="token__actions">
          <YmButton
            size="sm"
            variant="secondary"
            icon="refresh"
            :disabled="!enabled"
            @click="rotateToken"
          >
            {{ t("settings.localApi.rotate") }}
          </YmButton>
          <YmButton
            size="sm"
            variant="danger"
            icon="x"
            :disabled="!enabled"
            @click="revokeDialog = true"
          >
            {{ t("settings.localApi.revoke") }}
          </YmButton>
        </div>

        <p class="hint">{{ t("settings.localApi.tokenPlaceholderNote") }}</p>
        <YmNotice v-if="noticeText" tone="success"><bdi>{{ noticeText }}</bdi></YmNotice>
      </div>
    </YmCard>

    <YmCard>
      <div class="stats">
        <div class="stats__head">
          <div>
            <h2 class="section-title">{{ t("settings.localApi.statsTitle") }}</h2>
            <p class="hint">{{ t("settings.localApi.statsHint") }}</p>
          </div>
          <YmButton
            size="sm"
            variant="ghost"
            icon="refresh"
            :loading="loading"
            @click="load"
          >
            {{ t("settings.localApi.refresh") }}
          </YmButton>
        </div>

        <YmNotice v-if="errorText" tone="error"><bdi>{{ errorText }}</bdi></YmNotice>

        <template v-if="enabled">
          <div class="stats__total">
            <span class="stats__total-label">{{ t("settings.localApi.totalCalls") }}</span>
            <span class="stats__total-value mono">{{ stats?.totalCalls ?? 0 }}</span>
          </div>

          <table v-if="stats && stats.byEndpoint.length" class="table">
            <thead>
              <tr>
                <th>{{ t("settings.localApi.endpoint") }}</th>
                <th class="table__num">{{ t("settings.localApi.calls") }}</th>
                <th>{{ t("settings.localApi.lastError") }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in stats.byEndpoint" :key="row.endpoint">
                <td class="mono">{{ row.endpoint }}</td>
                <td class="table__num mono">{{ row.calls }}</td>
                <td class="table__error">{{ row.lastError ?? "—" }}</td>
              </tr>
            </tbody>
          </table>
          <p v-else class="empty">{{ t("settings.localApi.noCalls") }}</p>
        </template>

        <p v-else class="empty">{{ t("settings.localApi.disabledHint") }}</p>
      </div>
    </YmCard>

    <YmCard>
      <YmField
        :label="t('settings.localApi.concurrency')"
        :hint="t('settings.localApi.concurrencyHint')"
      >
        <div class="concurrency">
          <YmSelect
            :model-value="concurrency"
            :options="concurrencyOptions"
            @update:model-value="onConcurrency"
          />
        </div>
      </YmField>
    </YmCard>

    <YmDialog
      v-model="enableDialog"
      :title="t('settings.localApi.enableTitle')"
      :confirm-text="t('settings.localApi.enableConfirm')"
      :cancel-text="t('common.cancel')"
      @confirm="confirmEnable"
    >
      <p>{{ t("settings.localApi.enableBody") }}</p>
    </YmDialog>

    <YmDialog
      v-model="revokeDialog"
      :title="t('settings.localApi.revokeTitle')"
      :confirm-text="t('settings.localApi.revoke')"
      :cancel-text="t('common.cancel')"
      danger
      @confirm="confirmRevoke"
    >
      <p>{{ t("settings.localApi.revokeBody") }}</p>
    </YmDialog>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  max-width: 1240px;
}
.page__title {
  font-size: var(--font-size-xl);
}
.page__desc {
  margin-top: var(--space-1);
  color: var(--color-text-muted);
}
.stack {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.switch-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-5);
}
.switch-row__text {
  min-width: 0;
}
.switch-row__label {
  font-size: var(--font-size-sm);
  font-weight: 500;
  color: var(--color-text-muted);
}
.switch-row__hint {
  margin-top: 2px;
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.security {
  display: flex;
  gap: var(--space-2);
  align-items: flex-start;
  padding: var(--space-3);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
  color: var(--color-text-muted);
}
.security__title {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.security__value {
  margin-top: 1px;
  font-size: var(--font-size-sm);
  color: var(--color-text);
}
.token {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.token__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-4);
}
.section-title {
  font-size: var(--font-size-lg);
}
.hint {
  margin-top: 2px;
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.token__value {
  padding: var(--space-3);
  border: 1px dashed var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-bg);
  font-size: var(--font-size-sm);
  word-break: break-all;
}
.token__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
.notice {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-info);
  border-radius: var(--radius-md);
  background: var(--color-info-soft);
  font-size: var(--font-size-sm);
}
.stats {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.stats__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-4);
}
.stats__total {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  padding: var(--space-3);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
}
.stats__total-label {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.stats__total-value {
  font-size: var(--font-size-xl);
  font-weight: 600;
}
.table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--font-size-sm);
}
.table th,
.table td {
  padding: var(--space-2);
  text-align: left;
  border-bottom: 1px solid var(--color-border);
}
.table th {
  font-size: var(--font-size-xs);
  font-weight: 500;
  color: var(--color-text-faint);
}
.table__num {
  text-align: right;
  width: 80px;
}
.table__error {
  color: var(--color-text-muted);
}
.empty {
  padding: var(--space-4);
  text-align: center;
  font-size: var(--font-size-sm);
  color: var(--color-text-faint);
  border: 1px dashed var(--color-border);
  border-radius: var(--radius-md);
}
.concurrency {
  max-width: 220px;
}
.alert {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-error);
  border-radius: var(--radius-md);
  background: var(--color-error-soft);
  font-size: var(--font-size-sm);
}
</style>
