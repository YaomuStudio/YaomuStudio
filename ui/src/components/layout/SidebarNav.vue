<script setup lang="ts">
import { computed } from "vue";
import { useRoute } from "vue-router";
import { t } from "@/i18n";
import {
  evaluateAccess,
  type AccessKey,
  type AccessLevel,
} from "@/domain/capabilityAccess";
import { useCapabilitiesStore } from "@/stores/capabilities";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmTooltip from "@/components/ui/YmTooltip.vue";
import SidebarModelSummary from "./SidebarModelSummary.vue";

defineProps<{ collapsed: boolean }>();

interface NavItem {
  to: string;
  key: string;
  icon: string;
  /** 受发布准入控制的入口；缺省表示始终显示（工作台/项目/设置）。 */
  accessKey?: AccessKey;
}
interface NavGroup {
  key: string;
  labelKey: string;
  items: NavItem[];
}

const route = useRoute();

const groups: NavGroup[] = [
  {
    key: "work",
    labelKey: "nav.group.work",
    items: [
      { to: "/", key: "nav.workbench", icon: "home" },
      { to: "/projects", key: "nav.projects", icon: "folder" },
    ],
  },
  {
    key: "pipeline",
    labelKey: "nav.group.pipeline",
    items: [
      { to: "/pipeline/new", key: "nav.pipeline", icon: "film", accessKey: "pipeline" },
      { to: "/tools/custom", key: "nav.custom", icon: "link", accessKey: "tool-custom" },
    ],
  },
  {
    key: "tools",
    labelKey: "nav.group.tools",
    items: [
      { to: "/tools/separate", key: "nav.separate", icon: "layers", accessKey: "tool-separate" },
      { to: "/tools/extract", key: "nav.extract", icon: "subtitles", accessKey: "tool-extract" },
      { to: "/tools/erase", key: "nav.erase", icon: "eraser", accessKey: "tool-erase" },
      { to: "/tools/translate", key: "nav.translate", icon: "globe", accessKey: "tool-translate" },
      { to: "/voice-clone", key: "nav.voiceClone", icon: "mic", accessKey: "voice-clone" },
    ],
  },
  {
    key: "system",
    labelKey: "nav.group.system",
    items: [{ to: "/settings", key: "nav.settings", icon: "gear" }],
  },
];

const caps = useCapabilitiesStore();

function isActive(to: string): boolean {
  if (to === "/") return route.path === "/";
  return route.path === to || route.path.startsWith(`${to}/`);
}

function accessLevel(item: NavItem): AccessLevel | undefined {
  if (!item.accessKey) return undefined;
  return evaluateAccess(caps.slots, item.accessKey).level;
}

function navLabel(item: NavItem): string {
  if (accessLevel(item) === "setup") {
    return `${t(item.key)} · ${t("access.setup")}`;
  }
  return t(item.key);
}

// 能力尚未加载时槽位缺失按 setup 处理（显示）；加载后按真实准入过滤。
const visibleGroups = computed<NavGroup[]>(() =>
  groups
    .map((group) => ({
      ...group,
      items: group.items.filter((item) => {
        const level = accessLevel(item);
        return level === undefined || level !== "hidden";
      }),
    }))
    .filter((group) => group.items.length > 0),
);
</script>

<template>
  <nav
    id="primary-nav"
    class="sidebar"
    :class="{ 'sidebar--collapsed': collapsed }"
    aria-label="primary"
  >
    <!-- 品牌与折叠开关已上移到全宽标题栏（TopBar）。 -->

    <div v-scroll-fade class="sidebar__nav ym-scroll-fade">
      <div v-for="g in visibleGroups" :key="g.key" class="sidebar__group">
        <p v-if="!collapsed" class="sidebar__group-label">{{ t(g.labelKey) }}</p>
        <YmTooltip
          v-for="item in g.items"
          :key="item.to"
          :label="navLabel(item)"
          :disabled="!collapsed"
          placement="right"
        >
          <RouterLink
            :to="item.to"
            class="sidebar__item"
            :class="{ 'is-active': isActive(item.to) }"
            :data-access="accessLevel(item)"
            :aria-current="isActive(item.to) ? 'page' : undefined"
            :aria-label="collapsed ? navLabel(item) : undefined"
          >
            <YmIcon :name="item.icon" :size="16" />
            <span class="sidebar__item-label">{{ t(item.key) }}</span>
            <span
              v-if="accessLevel(item) === 'setup'"
              class="sidebar__item-badge"
              role="img"
              :aria-label="t('access.setupHint')"
              :title="t('access.setupHint')"
            />
          </RouterLink>
        </YmTooltip>
      </div>
    </div>
    <SidebarModelSummary :collapsed="collapsed" />
  </nav>
</template>

<style scoped>
.sidebar {
  display: flex;
  flex-direction: column;
  width: var(--sidebar-width);
  height: 100%;
  flex: none;
  overflow: hidden;
  border-right: 1px solid var(--color-border);
  background: var(--color-surface);
  transition:
    width var(--motion-duration-base) var(--motion-ease-snap),
    padding var(--motion-duration-base) var(--motion-ease-snap);
}
.sidebar--collapsed {
  width: var(--sidebar-collapsed-width);
}
.sidebar__nav {
  flex: 1;
  min-height: 0;
  padding: 14px 10px 16px;
  overflow-y: auto;
}
.sidebar--collapsed .sidebar__nav {
  padding: 14px 12px 16px;
}
.sidebar__group {
  margin-bottom: 20px;
}
.sidebar--collapsed .sidebar__group {
  margin-bottom: 8px;
}
.sidebar--collapsed .sidebar__group + .sidebar__group {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--color-border);
}
.sidebar__group:last-child {
  margin-bottom: 4px;
}
.sidebar__group-label {
  margin: 0 0 6px 10px;
  font-size: var(--font-size-xs);
  font-weight: 600;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--color-text-faint);
}
.sidebar--collapsed .sidebar__group-label {
  display: none;
}
.sidebar :deep(.ym-tooltip__trigger) {
  display: block;
  width: 100%;
}
.sidebar--collapsed :deep(.ym-tooltip__trigger) {
  width: 40px;
  margin: 0 auto;
}
.sidebar__item {
  position: relative;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border-radius: 8px;
  color: var(--color-text-muted);
  font-size: var(--font-size-md);
  text-decoration: none;
  transition:
    background var(--motion-duration-snap) var(--motion-ease-snap),
    color var(--motion-duration-snap) var(--motion-ease-snap);
}
.sidebar--collapsed .sidebar__item {
  width: 40px;
  height: 40px;
  padding: 0;
  justify-content: center;
  border-radius: 10px;
}
.sidebar__item:hover {
  background: var(--color-surface-2);
  color: var(--color-text);
}
.sidebar__item.is-active {
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
  font-weight: 600;
}
.sidebar__item.is-active::before {
  content: "";
  position: absolute;
  left: -10px;
  top: 22%;
  bottom: 22%;
  width: 3px;
  border-radius: var(--radius-pill);
  background: var(--gradient-primary);
}
.sidebar--collapsed .sidebar__item.is-active::before {
  left: -6px;
}
.sidebar__item-label {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.sidebar--collapsed .sidebar__item-label {
  display: none;
}
.sidebar__item-badge {
  position: absolute;
  top: 50%;
  right: 8px;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-warning);
  transform: translateY(-50%);
}
.sidebar--collapsed .sidebar__item-badge {
  top: 50%;
  right: 7px;
  transform: translateY(-50%);
}
@media (prefers-reduced-motion: reduce) {
  .sidebar,
  .sidebar__item {
    transition: none;
  }
}
</style>
