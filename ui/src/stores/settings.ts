// 设置 store：主题/语言/缩放/通知/隐私等，持久化到 localStorage（FR-SET-*）。
import { defineStore } from "pinia";
import { ref, watch } from "vue";
import { setLocale, detectSystemLocale, type Locale } from "@/i18n";

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
  onboarded: boolean;
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
    return raw ? (JSON.parse(raw) as Partial<Persisted>) : {};
  } catch {
    return {};
  }
}

const media = window.matchMedia("(prefers-color-scheme: dark)");

export const useSettingsStore = defineStore("settings", () => {
  const saved = loadPersisted();

  const locale = ref<Locale>(saved.locale ?? detectSystemLocale());
  const theme = ref<ThemeMode>(saved.theme ?? "system");
  const uiScale = ref<number>(saved.uiScale ?? 100);
  const onboarded = ref<boolean>(saved.onboarded ?? false);
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
    const bridge = window.yaomu?.api;
    if (bridge && typeof bridge.setUiScale === "function") {
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

  function persist(): void {
    const data: Persisted = {
      locale: locale.value,
      theme: theme.value,
      uiScale: uiScale.value,
      onboarded: onboarded.value,
      notifications: notifications.value,
      startup: startup.value,
      closeBehavior: closeBehavior.value,
      autoCheckUpdates: autoCheckUpdates.value,
      autoDownloadUpdates: autoDownloadUpdates.value,
      telemetry: telemetry.value,
      device: device.value,
    };
    localStorage.setItem(STORAGE_KEY, JSON.stringify(data));
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
  const desktopApi = window.yaomu?.api;
  if (desktopApi && typeof desktopApi.setCloseBehavior === "function") {
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
  if (desktopApi && typeof desktopApi.setAutoCheckUpdates === "function") {
    watch(autoCheckUpdates, (value) => {
      void desktopApi.setAutoCheckUpdates(value).catch(() => undefined);
    });
    if (!hasSavedAutoCheckUpdates && typeof desktopApi.getAutoCheckUpdates === "function") {
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
  if (desktopApi && typeof desktopApi.setAutoDownloadUpdates === "function") {
    watch(autoDownloadUpdates, (value) => {
      void desktopApi.setAutoDownloadUpdates(value).catch(() => undefined);
    });
    if (
      !hasSavedAutoDownloadUpdates &&
      typeof desktopApi.getAutoDownloadUpdates === "function"
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
    markOnboarded() {
      onboarded.value = true;
    },
  };
});
