// 设置 store：主题/语言/缩放/通知/隐私等，持久化到 localStorage（FR-SET-*）。
import { defineStore } from "pinia";
import { ref, watch } from "vue";
import { setLocale, detectSystemLocale, type Locale } from "@/i18n";
import {
  hasDesktopMethod,
  safeDesktopApi,
} from "@/api/bridge-access";

export type ThemeMode = "light" | "dark" | "system";
export type CloseBehavior = "quit" | "minimize";
export type StartupMode = "workbench" | "lastProject";

export interface NotificationPrefs {
  taskDone: boolean;
  taskFailed: boolean;
  downloadDone: boolean;
  updateAvailable: boolean;
}

const STORAGE_KEY = "yaomu.settings";

interface Persisted {
  locale: Locale;
  theme: ThemeMode;
  uiScale: number;
  /** 旧版本的引导标记；仅用于迁移到主进程 onboarding.json，桌面端不再写回。 */
  onboarded?: boolean;
  notifications: NotificationPrefs;
  startup: StartupMode;
  closeBehavior: CloseBehavior;
  autoCheckUpdates: boolean;
  autoDownloadUpdates: boolean;
  telemetry: boolean;
  device: string;
}

function loadPersisted(): Partial<Persisted> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    return parsed as Partial<Persisted>;
  } catch {
    return {};
  }
}

const media = window.matchMedia("(prefers-color-scheme: dark)");

export const useSettingsStore = defineStore("settings", () => {
  const saved = loadPersisted();

  // 引导状态：桌面端以主进程 `<userData>/onboarding.json` 为唯一权威，
  // localStorage 仅作为旧版本的一次性迁移来源。旧版 preload 没有新方法时
  // （或浏览器预览/E2E 无桥）继续使用 localStorage，避免升级后误重走引导。
  const desktopApi = safeDesktopApi();
  const mainOnboardingAuthority =
    hasDesktopMethod(desktopApi, "getOnboardingCompleted") &&
    hasDesktopMethod(desktopApi, "setOnboardingCompleted");

  const locale = ref<Locale>(saved.locale ?? detectSystemLocale());
  const theme = ref<ThemeMode>(saved.theme ?? "system");
  const uiScale = ref<number>(saved.uiScale ?? 100);
  const onboarded = ref<boolean>(
    typeof saved.onboarded === "boolean" ? saved.onboarded : false,
  );
  const notifications = ref<NotificationPrefs>({
    taskDone: true,
    taskFailed: true,
    downloadDone: true,
    updateAvailable: true,
    ...saved.notifications,
  });
  const startup = ref<StartupMode>(saved.startup ?? "workbench");
  const closeBehavior = ref<CloseBehavior>(saved.closeBehavior ?? "quit");
  // localStorage 可能来自旧版本/被清空；只有明确的布尔值才算用户已持久化的选择。
  const savedAutoCheckUpdates = saved.autoCheckUpdates;
  const hasSavedAutoCheckUpdates = typeof savedAutoCheckUpdates === "boolean";
  const autoCheckUpdates = ref<boolean>(
    hasSavedAutoCheckUpdates ? savedAutoCheckUpdates : true,
  );
  // 自动下载默认关闭；只有明确持久化的布尔值才算用户选择。
  const savedAutoDownloadUpdates = saved.autoDownloadUpdates;
  const hasSavedAutoDownloadUpdates = typeof savedAutoDownloadUpdates === "boolean";
  const autoDownloadUpdates = ref<boolean>(
    hasSavedAutoDownloadUpdates ? savedAutoDownloadUpdates : false,
  );
  const telemetry = ref<boolean>(saved.telemetry ?? false);
  const device = ref<string>(saved.device ?? "auto");

  function resolvedTheme(): "light" | "dark" {
    if (theme.value === "system") return media.matches ? "dark" : "light";
    return theme.value;
  }

  function applyTheme(): void {
    document.documentElement.setAttribute("data-theme", resolvedTheme());
  }

  function applyScale(): void {
    const factor = uiScale.value / 100;
    const bridge = safeDesktopApi();
    if (bridge && hasDesktopMethod(bridge, "setUiScale")) {
      // 桌面端用 Chromium 页面缩放：视口、媒体查询、fixed 定位和 100% 高度
      // 都按缩放后的真实尺寸计算；CSS zoom 会把布局按比例撑出窗口。
      document.documentElement.style.removeProperty("zoom");
      void bridge.setUiScale(factor).catch(() => undefined);
      return;
    }
    // 浏览器预览与 E2E 没有桌面桥：保留 CSS zoom 回退，行为与旧版一致。
    document.documentElement.style.setProperty("zoom", String(factor));
  }

  function setLocaleAndApply(next: Locale): void {
    locale.value = next;
    setLocale(next);
  }

  /** 旧版引导标记等待主进程写入成功；失败期间 persist 继续保留该键以便下次重试。 */
  let legacyOnboardedPending = false;

  function persist(): void {
    const data: Persisted = {
      locale: locale.value,
      theme: theme.value,
      uiScale: uiScale.value,
      notifications: notifications.value,
      startup: startup.value,
      closeBehavior: closeBehavior.value,
      autoCheckUpdates: autoCheckUpdates.value,
      autoDownloadUpdates: autoDownloadUpdates.value,
      telemetry: telemetry.value,
      device: device.value,
    };
    // 桌面端引导状态由主进程文件持有；localStorage 不再写入该键。
    // 例外：迁移写入失败后保留旧键，避免本次会话的任何设置变更提前删掉重试依据。
    if (!mainOnboardingAuthority || legacyOnboardedPending) data.onboarded = onboarded.value;
    localStorage.setItem(STORAGE_KEY, JSON.stringify(data));
  }

  /** 迁移后从 localStorage 持久对象移除旧引导键；不再把它写回权威来源。 */
  function removeLegacyOnboarded(): void {
    try {
      const raw = localStorage.getItem(STORAGE_KEY);
      if (!raw) return;
      const data = JSON.parse(raw) as Record<string, unknown>;
      if (!Object.prototype.hasOwnProperty.call(data, "onboarded")) return;
      delete data.onboarded;
      localStorage.setItem(STORAGE_KEY, JSON.stringify(data));
    } catch {
      // 存储不可用或内容损坏：旧键不会被再次写入，忽略清理失败。
    }
  }

  /**
   * 桌面端引导状态水合：必须在路由初始导航前 await，避免先渲染引导/工作台再跳转。
   * 主进程给出布尔值时以它为准；`null`（安装器未写、文件损坏或 IPC 失败）时
   * 从旧版 localStorage 迁移一次：旧值 true 才写主进程，迁移成功后删除旧键。
   * 浏览器预览/E2E 无桥时保持既有 localStorage 行为。
   */
  async function hydrateOnboarding(): Promise<void> {
    if (!mainOnboardingAuthority || !desktopApi) return;
    let completed: boolean | null | undefined;
    try {
      const value = await desktopApi.getOnboardingCompleted();
      completed =
        typeof value === "boolean" ? value : value === null ? null : undefined;
    } catch {
      // 读取失败不能被当成“主进程明确未设置”：保留旧值等待下次重试。
      if (saved.onboarded === true) legacyOnboardedPending = true;
      return;
    }
    if (completed === undefined) {
      // 旧桥/不兼容桥可能存在方法但返回另一种形状。不要把它当 null 后
      // 删除旧 localStorage；保留旧值，下一次启动仍可重试。
      if (saved.onboarded === true) legacyOnboardedPending = true;
      return;
    }
    if (typeof completed === "boolean") {
      onboarded.value = completed;
      // 旧版本写下的标记不再是权威来源，顺手清理。
      removeLegacyOnboarded();
      return;
    }
    if (saved.onboarded !== true) {
      removeLegacyOnboarded();
      return;
    }
    legacyOnboardedPending = true;
    try {
      await desktopApi.setOnboardingCompleted();
      onboarded.value = true;
      legacyOnboardedPending = false;
      removeLegacyOnboarded();
    } catch {
      // 写入失败时保留旧键供下次启动重试迁移；本次仍视为已完成，不误伤升级用户。
      // 设置 `legacyOnboardedPending` 防止本次会话后续的 persist 提前删除旧键。
      legacyOnboardedPending = true;
      console.warn("引导状态迁移失败，保留本地旧值以便下次重试");
    }
  }

  // 初始化应用
  setLocaleAndApply(locale.value);
  applyTheme();
  applyScale();

  watch([locale, theme, uiScale, onboarded, notifications, startup, closeBehavior, autoCheckUpdates, autoDownloadUpdates, telemetry, device], persist, { deep: true });
  watch(theme, applyTheme);
  watch(uiScale, applyScale);

  // 关闭行为以主进程为唯一权威（决定是否拦截窗口 close 与创建托盘）。
  // 初始化同步一次，之后设置页变更时继续同步；浏览器 mock 没有桌面桥时静默跳过。
  // 主进程写入失败不回滚 localStorage，也不阻塞设置页交互。
  if (desktopApi && hasDesktopMethod(desktopApi, "setCloseBehavior")) {
    watch(
      closeBehavior,
      (value) => {
        void desktopApi.setCloseBehavior(value).catch(() => undefined);
      },
      { immediate: true },
    );
  }

  // 自动检查更新以主进程为权威：localStorage 有持久布尔值时同步一次；
  // 没有持久值时（新配置/清空缓存）先通过只读 IPC 从主进程水合，
  // 避免界面默认 true 写回覆盖主进程已保存的 false。浏览器 mock / 旧桥静默跳过。
  if (desktopApi && hasDesktopMethod(desktopApi, "setAutoCheckUpdates")) {
    watch(autoCheckUpdates, (value) => {
      void desktopApi.setAutoCheckUpdates(value).catch(() => undefined);
    });
    if (!hasSavedAutoCheckUpdates && hasDesktopMethod(desktopApi, "getAutoCheckUpdates")) {
      const initialAutoCheckUpdates = autoCheckUpdates.value;
      void desktopApi
        .getAutoCheckUpdates()
        .then((value) => {
          // 水合返回前用户已操作则保留用户选择，避免异步结果覆盖。
          if (autoCheckUpdates.value !== initialAutoCheckUpdates) return;
          if (typeof value === "boolean") autoCheckUpdates.value = value;
        })
        .catch(() => undefined);
    } else {
      void desktopApi.setAutoCheckUpdates(autoCheckUpdates.value).catch(() => undefined);
    }
  }

  // 自动下载以主进程为权威：与自动检查相同的持久值/水合策略。
  if (desktopApi && hasDesktopMethod(desktopApi, "setAutoDownloadUpdates")) {
    watch(autoDownloadUpdates, (value) => {
      void desktopApi.setAutoDownloadUpdates(value).catch(() => undefined);
    });
    if (
      !hasSavedAutoDownloadUpdates &&
      hasDesktopMethod(desktopApi, "getAutoDownloadUpdates")
    ) {
      const initialAutoDownloadUpdates = autoDownloadUpdates.value;
      void desktopApi
        .getAutoDownloadUpdates()
        .then((value) => {
          if (autoDownloadUpdates.value !== initialAutoDownloadUpdates) return;
          if (typeof value === "boolean") autoDownloadUpdates.value = value;
        })
        .catch(() => undefined);
    } else {
      void desktopApi.setAutoDownloadUpdates(autoDownloadUpdates.value).catch(() => undefined);
    }
  }

  media.addEventListener("change", () => {
    if (theme.value === "system") applyTheme();
  });

  return {
    locale,
    theme,
    uiScale,
    onboarded,
    notifications,
    startup,
    closeBehavior,
    autoCheckUpdates,
    autoDownloadUpdates,
    telemetry,
    device,
    resolvedTheme,
    setLocaleAndApply,
    setTheme(next: ThemeMode) {
      theme.value = next;
    },
    setUiScale(next: number) {
      uiScale.value = Math.min(150, Math.max(80, next));
    },
    hydrateOnboarding,
    /**
     * 标记引导完成。桌面端写入主进程权威文件；失败记录但不回滚界面值，
     * 下次启动若主进程仍未完成会按安全方向重新引导。
     */
    async markOnboarded(): Promise<void> {
      onboarded.value = true;
      if (!mainOnboardingAuthority || !desktopApi) return;
      legacyOnboardedPending = true;
      try {
        await desktopApi.setOnboardingCompleted();
        // 主进程已持有完成状态，旧迁移键不再需要重试保留。
        legacyOnboardedPending = false;
      } catch {
        console.warn("引导状态保存失败");
      }
    },
  };
});
