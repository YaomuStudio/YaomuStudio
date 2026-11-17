<script setup lang="ts">
// 已准入但未就绪时的统一提示：入口保留，说明需要安装/修复，不宣称已可用。
import { computed } from "vue";
import { useRouter } from "vue-router";
import { t } from "@/i18n";
import {
  evaluateAccess,
  slotLabelKey,
  type AccessKey,
  type AccessResult,
} from "@/domain/capabilityAccess";
import { useCapabilitiesStore } from "@/stores/capabilities";
import YmButton from "@/components/ui/YmButton.vue";
import YmNotice from "@/components/ui/YmNotice.vue";

const props = defineProps<{ accessKey: AccessKey; access?: AccessResult }>();

const router = useRouter();
const caps = useCapabilitiesStore();
const result = computed(() => props.access ?? evaluateAccess(caps.slots, props.accessKey));
const setupLabels = computed(() =>
  result.value.setup.map(slotLabelKey).map((key) => t(key)).join(" · "),
);
/** FFmpeg 随应用打包，模型下载无法修复；未就绪时提示重装，不宣称已可用。 */
const includesRuntimeComponent = computed(() => result.value.setup.includes("ffmpeg"));

/**
 * 只导航到既有模型/环境页，不自动安装或下载。
 * 显式携带未就绪槽位，供模型页突出本次入口相关的行；没有上下文时不猜推荐项。
 */
function openModels(): void {
  void router.push({
    path: "/settings/models",
    query: result.value.setup.length ? { slots: result.value.setup.join(",") } : {},
  });
}
</script>

<template>
  <YmNotice v-if="result.level === 'setup'" tone="warning" :title="t('access.setupHint')">
    <div class="access-notice__text">
      <span v-if="setupLabels" class="access-notice__slots">
        {{ t("access.setupSlots", { slots: setupLabels }) }}
      </span>
      <span v-if="includesRuntimeComponent" class="access-notice__runtime">
        {{ t("access.runtimeComponentHint") }}
      </span>
      <div class="access-notice__actions">
        <YmButton size="sm" variant="secondary" icon="gear" @click="openModels">
          {{ t("access.viewModels") }}
        </YmButton>
      </div>
    </div>
  </YmNotice>
</template>

<style scoped>
.access-notice__text {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
  line-height: var(--line-height-normal);
}
.access-notice__slots,
.access-notice__runtime {
  color: var(--color-text-muted);
}
.access-notice__actions {
  display: flex;
  margin-top: 2px;
}
</style>
