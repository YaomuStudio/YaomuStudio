<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { t } from "@/i18n";
import { useUpdater } from "../composables/useUpdater";

const {
  phase,
  version,
  installState,
  installWaiting,
  canCancelInstall,
  install,
  cancelInstall,
} = useUpdater();

// 只在下载完成时提示一次；“稍后”只关闭本次提示，不改变全局 downloaded 状态。
const dismissed = ref(false);
watch(phase, () => {
  dismissed.value = false;
});

const visible = computed(() => phase.value === "downloaded" && !dismissed.value);
const waitingText = computed(() =>
  t("update.toast.waiting", { version: installState.value?.version ?? version.value }),
);
</script>

<template>
  <div v-if="visible" class="update-toast" role="status" data-testid="update-toast">
    <p v-if="installWaiting">{{ waitingText }}</p>
    <p v-else>{{ t("update.toast.downloaded", { version }) }}</p>
    <div class="actions">
      <button v-if="!installWaiting" type="button" data-testid="update-toast-install" @click="install">
        {{ t("update.toast.install") }}
      </button>
      <button
        type="button"
        v-if="canCancelInstall"
        data-testid="update-toast-cancel"
        @click="cancelInstall"
      >
        {{ t("update.toast.cancel") }}
      </button>
      <button type="button" data-testid="update-toast-later" @click="dismissed = true">
        {{ t("update.toast.later") }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.update-toast {
  position: fixed;
  right: 16px;
  bottom: 16px;
  width: 320px;
  padding: 12px 14px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  color: var(--color-text);
  font-size: var(--font-size-md);
  box-shadow: var(--shadow-md);
  z-index: 1000;
}
.update-toast p {
  margin: 0 0 8px;
}
.actions {
  display: flex;
  gap: 8px;
}
.actions button {
  padding: 4px 12px;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
  color: var(--color-text);
  cursor: pointer;
}
.actions button:hover {
  background: var(--color-surface-3);
}
</style>
