// 新建页提交后闭环：主按钮切换到「查看详情」，并支持连续创建。
import { computed } from "vue";
import { useRouter } from "vue-router";
import { t } from "@/i18n";

export function useCreationFlow(options: {
  /** 已创建的 Run 所属项目；为空表示还在编辑原始表单。 */
  projectId: () => string | undefined;
  busy: () => boolean;
  create: () => void;
  reset: () => void;
}) {
  const router = useRouter();
  const hasProject = computed(() => Boolean(options.projectId()));
  /** 运行中主按钮仍是创建（禁用），只用次级「查看详情」离开，避免两个同名按钮。 */
  const canOpenProject = computed(() => hasProject.value && !options.busy());

  const primaryLabel = computed(() =>
    canOpenProject.value ? t("common.seeDetails") : t("common.create"),
  );

  async function openProject(): Promise<void> {
    const id = options.projectId();
    if (!id) return;
    await router.push(`/projects/${id}`);
  }

  function onPrimary(): void {
    if (hasProject.value && !options.busy()) {
      void openProject();
      return;
    }
    options.create();
  }

  function startNew(): void {
    options.reset();
  }

  return { hasProject, canOpenProject, primaryLabel, onPrimary, openProject, startNew };
}
