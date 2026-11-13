<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { t } from "@/i18n";
import logoAnimUrl from "@/assets/logo-anim.svg";
import logoStaticUrl from "@/assets/logo.svg";
import YmIcon from "@/components/ui/YmIcon.vue";
import NotificationCenter from "@/components/NotificationCenter.vue";
import UpdateIndicator from "./UpdateIndicator.vue";

defineProps<{ sidebarOpen: boolean }>();
const emit = defineEmits<{ (e: "toggle-sidebar"): void }>();

/*
 * 全宽标题栏：品牌 + 侧栏开关居左，更新/通知居右。
 * mac 红黄绿按钮浮在左侧留白区（widgets.css os-mac 预留 inset）；
 * win 由系统 WCO 在右侧绘制控件；linux 在此自绘与 WCO 视觉一致的按钮。
 */
const platform = typeof window !== "undefined" ? window.yaomu?.platform : undefined;
const isLinuxDesktop = platform === "linux";

const prefersReducedMotion = ref(false);
let motionQuery: MediaQueryList | null = null;
function syncMotion(query: MediaQueryListEvent): void {
  prefersReducedMotion.value = query.matches;
}
onMounted(() => {
  motionQuery = window.matchMedia("(prefers-reduced-motion: reduce)");
  prefersReducedMotion.value = motionQuery.matches;
  motionQuery.addEventListener("change", syncMotion);
});
onBeforeUnmount(() => {
  motionQuery?.removeEventListener("change", syncMotion);
});
const logoUrl = computed(() => (prefersReducedMotion.value ? logoStaticUrl : logoAnimUrl));

const maximized = ref(false);
let maximizePoll: ReturnType<typeof setInterval> | null = null;
onMounted(() => {
  if (!isLinuxDesktop || !window.yaomu?.window) return;
  const win = window.yaomu.window;
  void win.isMaximized().then((value) => {
    maximized.value = value;
  });
  maximizePoll = setInterval(() => {
    void win.isMaximized().then((value) => {
      maximized.value = value;
    });
  }, 800);
});
onBeforeUnmount(() => {
  if (maximizePoll) clearInterval(maximizePoll);
});

function minimizeApp(): void {
  void window.yaomu?.window?.minimize();
}
function closeApp(): void {
  void window.yaomu?.window?.close();
}
function toggleMaximize(): void {
  void window.yaomu?.window?.toggleMaximize();
}
</script>

<template>
  <header class="topbar">
    <div class="topbar__left">
      <RouterLink to="/" class="topbar__brand" :aria-label="t('app.name')">
        <img class="topbar__logo" :src="logoUrl" alt="" aria-hidden="true" />
        <span class="topbar__brand-name">{{ t("app.name") }}</span>
      </RouterLink>
      <button
        class="topbar__toggle"
        type="button"
        :aria-label="sidebarOpen ? t('nav.collapseSidebar') : t('nav.expandSidebar')"
        :title="sidebarOpen ? t('nav.collapseSidebar') : t('nav.expandSidebar')"
        :aria-expanded="sidebarOpen"
        aria-controls="primary-nav"
        @click="emit('toggle-sidebar')"
      >
        <YmIcon :name="sidebarOpen ? 'sidebar-collapse' : 'sidebar-expand'" :size="18" />
      </button>
    </div>

    <div class="topbar__right">
      <UpdateIndicator />
      <NotificationCenter />
      <div v-if="isLinuxDesktop" class="topbar__wincaps" role="group" :aria-label="t('app.name')">
        <button class="wincap" type="button" aria-label="Minimize" @click="minimizeApp">
          <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true"><path d="M0 5h10" stroke="currentColor" stroke-width="1" /></svg>
        </button>
        <button class="wincap" type="button" aria-label="Maximize" @click="toggleMaximize">
          <svg v-if="maximized" width="10" height="10" viewBox="0 0 10 10" aria-hidden="true" fill="none">
            <rect x="0.5" y="2.5" width="7" height="7" stroke="currentColor" stroke-width="1" />
            <path d="M2.5 2.5V0.5h7v7h-2" stroke="currentColor" stroke-width="1" />
          </svg>
          <svg v-else width="10" height="10" viewBox="0 0 10 10" aria-hidden="true" fill="none">
            <rect x="0.5" y="0.5" width="9" height="9" stroke="currentColor" stroke-width="1" />
          </svg>
        </button>
        <button class="wincap wincap--close" type="button" aria-label="Close" @click="closeApp">
          <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true"><path d="M0 0l10 10M10 0L0 10" stroke="currentColor" stroke-width="1" /></svg>
        </button>
      </div>
    </div>
  </header>
</template>

<style scoped>
.topbar {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: var(--topbar-height);
  padding: 0 16px;
  background: var(--color-surface);
  border-bottom: 1px solid var(--color-border);
  flex: none;
}
.topbar::after {
  content: "";
  position: absolute;
  left: 0;
  right: 0;
  bottom: -1px;
  height: 1px;
  background: linear-gradient(90deg, transparent, var(--color-primary), transparent);
  opacity: 0.35;
  pointer-events: none;
}
.topbar__left {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
}
.topbar__brand {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  text-decoration: none;
}
.topbar__brand:hover {
  text-decoration: none;
}
.topbar__logo {
  width: 28px;
  height: 30px;
  display: block;
  flex: none;
}
.topbar__brand-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 0.01em;
  background: var(--gradient-brand);
  -webkit-background-clip: text;
  background-clip: text;
  -webkit-text-fill-color: transparent;
  color: var(--color-primary);
}
.topbar__toggle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border-radius: 8px;
  color: var(--color-text-muted);
  transition:
    background var(--motion-duration-snap) var(--motion-ease-snap),
    color var(--motion-duration-snap) var(--motion-ease-snap);
}
.topbar__toggle:hover {
  background: var(--color-surface-2);
  color: var(--color-text);
}
.topbar__right {
  display: flex;
  align-items: center;
  gap: var(--space-4);
}
/* Linux 自绘窗口控件：与 Windows WCO 同视觉语言（扁平 46×52 热区、悬停底色）。 */
.topbar__wincaps {
  display: flex;
  align-items: stretch;
  height: var(--topbar-height);
  margin-right: -16px;
  margin-left: var(--space-2);
}
.wincap {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 46px;
  height: 100%;
  color: var(--color-text-muted);
  background: transparent;
  transition: background var(--motion-duration-snap) var(--motion-ease-snap);
}
.wincap:hover {
  background: var(--color-surface-2);
  color: var(--color-text);
}
.wincap--close:hover {
  background: #e81123;
  color: #fff;
}
</style>
