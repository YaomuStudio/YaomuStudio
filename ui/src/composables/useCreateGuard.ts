// 新建页准入守卫：引擎未就绪时禁用创建，并给出缺少的能力文案。
// 与页面上的 CapabilityAccessNotice 使用同一份准入判定，避免按钮可点但预检必失败。
import { computed } from "vue";
import { t } from "@/i18n";
import {
  evaluateAccess,
  slotLabelKey,
  type AccessKey,
} from "@/domain/capabilityAccess";
import { useCapabilitiesStore } from "@/stores/capabilities";

export function useCreateGuard(accessKey: AccessKey) {
  const caps = useCapabilitiesStore();

  /** 能力目录未加载时不拦截（Core 预检仍是最终权威）。 */
  const access = computed(() =>
    caps.slots.length > 0 ? evaluateAccess(caps.slots, accessKey) : undefined,
  );
  const blocked = computed(
    () => access.value !== undefined && access.value.level !== "ready",
  );
  const blockedText = computed(() => {
    const current = access.value;
    if (!current || current.level === "ready") return "";
    const slots = current.setup.length ? current.setup : current.missing;
    const labels = slots.map(slotLabelKey).map((key) => t(key)).join(" · ");
    return labels
      ? t("creation.footer.missing", { items: labels })
      : t("access.setupHint");
  });

  return { blocked, blockedText };
}
