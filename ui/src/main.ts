import { createApp } from "vue";
import { createPinia } from "pinia";
import App from "./App.vue";
import { router } from "./router";
import "./styles/tokens.css";
import "./styles/base.css";
import "./styles/widgets.css";
import "./i18n/register";
import { evaluateAccess, type AccessKey } from "./domain/capabilityAccess";
import { useCapabilitiesStore } from "./stores/capabilities";
import { useRuntimeStore } from "./stores/runtime";
import { useSettingsStore } from "./stores/settings";
import { scrollFade } from "./directives/scrollFade";

// 桌面端无边框窗口标记：所有页面（含首次引导）都需要，不能只挂 AppShell。
const desktopPlatform = window.yaomu?.platform;
if (desktopPlatform === "darwin" || desktopPlatform === "win32" || desktopPlatform === "linux") {
  const osClass =
    desktopPlatform === "darwin" ? "os-mac" : desktopPlatform === "win32" ? "os-win" : "os-linux";
  document.documentElement.classList.add("is-frameless", osClass);
}

const app = createApp(App);
const pinia = createPinia();
app.use(pinia);

// 宿主就绪桥：先订阅再取初始快照；窗口先可见，业务页由 App 门控。
const runtime = useRuntimeStore(pinia);
void runtime.init();

app.use(router);
app.directive("scroll-fade", scrollFade);

// 首次引导门控：未完成 → /onboarding；已完成则跳过 onboarding。
router.beforeEach(async (to) => {
  const settings = useSettingsStore(pinia);
  if (!settings.onboarded && to.name !== "onboarding") return { name: "onboarding" };
  if (settings.onboarded && to.name === "onboarding") return { name: "workbench" };

  // 宿主首次 ready 前不读取能力目录：初始化面板不挂载业务页，
  // 旧完整桥/演示/不可用模式（gated=false）保持原守卫行为。
  if (runtime.gated && !runtime.firstReady) return true;

  // 发布准入：受控入口在首次进入前等待能力目录；Core 不可用时保留入口，
  // 由页面展示现有错误态，不能因为加载失败把全部功能隐藏。
  const accessKey = to.meta.accessKey as AccessKey | undefined;
  if (!accessKey) return true;
  const caps = useCapabilitiesStore(pinia);
  if (caps.slots.length === 0) {
    try {
      await caps.load();
    } catch {
      return true;
    }
  }
  if (evaluateAccess(caps.slots, accessKey).level === "hidden") {
    return { name: "workbench" };
  }
  return true;
});

app.mount("#app");
