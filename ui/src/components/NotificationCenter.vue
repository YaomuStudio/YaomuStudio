<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { formatDate, t } from "@/i18n";
import YmIcon from "@/components/ui/YmIcon.vue";
import {
  useNotificationsStore,
  type AppNotification,
  type NotificationKind,
} from "@/stores/notifications";

const router = useRouter();
const notifications = useNotificationsStore();

const open = ref(false);
const root = ref<HTMLElement | null>(null);
const filter = ref<NotificationKind | "all">("all");

const FILTERS = computed(() => [
  { key: "all" as const, label: t("notify.filter.all") },
  { key: "task" as const, label: t("notify.filter.task") },
  { key: "update" as const, label: t("notify.filter.update") },
  { key: "system" as const, label: t("notify.filter.system") },
]);

const filtered = computed(() =>
  filter.value === "all"
    ? notifications.items
    : notifications.items.filter((item) => item.kind === filter.value),
);

const unreadByKind = computed(() => {
  const map = new Map<NotificationKind, number>();
  for (const item of notifications.items) {
    if (notifications.readIds.has(item.id)) continue;
    map.set(item.kind, (map.get(item.kind) ?? 0) + 1);
  }
  return map;
});

function toggle(): void {
  open.value = !open.value;
}

function closeOnOutside(event: MouseEvent): void {
  if (root.value && !root.value.contains(event.target as Node)) open.value = false;
}

onMounted(() => document.addEventListener("mousedown", closeOnOutside));
onBeforeUnmount(() => document.removeEventListener("mousedown", closeOnOutside));

function kindIcon(kind: AppNotification["kind"]): string {
  if (kind === "task") return "list";
  if (kind === "update") return "download";
  return "info";
}

function toneClass(item: AppNotification): string {
  return `notif--${item.tone}`;
}

function openItem(item: AppNotification): void {
  notifications.markRead(item.id);
  if (item.route) {
    open.value = false;
    void router.push(item.route);
  }
}
</script>

<template>
  <div ref="root" class="notif-center">
    <button
      type="button"
      class="notif-center__trigger"
      :aria-label="t('notify.title')"
      :title="t('notify.title')"
      :aria-expanded="open"
      @click="toggle"
    >
      <YmIcon name="bell" :size="16" />
      <span v-if="notifications.unreadCount" class="notif-center__badge">
        {{ notifications.unreadCount > 99 ? "99+" : notifications.unreadCount }}
      </span>
    </button>

    <div v-if="open" class="notif-center__panel" role="dialog" :aria-label="t('notify.title')">
      <header class="notif-center__head">
        <h3 class="notif-center__title">{{ t("notify.title") }}</h3>
        <button
          v-if="notifications.unreadCount"
          type="button"
          class="notif-center__mark-all"
          @click="notifications.markAllRead()"
        >
          {{ t("notify.markAllRead") }}
        </button>
      </header>

      <div class="notif-center__filters" role="tablist">
        <button
          v-for="f in FILTERS"
          :key="f.key"
          type="button"
          role="tab"
          class="notif-center__filter"
          :class="{ 'is-active': filter === f.key }"
          :aria-selected="filter === f.key"
          @click="filter = f.key"
        >
          {{ f.label }}
          <span
            v-if="f.key !== 'all' && unreadByKind.get(f.key as NotificationKind)"
            class="notif-center__filter-dot"
          ></span>
        </button>
      </div>

      <div v-scroll-fade class="notif-center__list ym-scroll-fade">
        <button
          v-for="item in filtered"
          :key="item.id"
          type="button"
          class="notif"
          :class="[toneClass(item), { 'is-unread': !notifications.readIds.has(item.id) }]"
          @click="openItem(item)"
        >
          <span class="notif__icon"><YmIcon :name="kindIcon(item.kind)" :size="14" /></span>
          <span class="notif__body">
            <span class="notif__title">{{ item.title }}</span>
            <span class="notif__desc">{{ item.body }}</span>
            <span class="notif__time">{{ formatDate(item.createdAt) }}</span>
          </span>
          <span v-if="!notifications.readIds.has(item.id)" class="notif__dot"></span>
        </button>

        <div v-if="!filtered.length" class="notif-center__empty">
          <span class="notif-center__empty-icon"><YmIcon name="bell" :size="24" /></span>
          <p class="notif-center__empty-title">{{ t("notify.empty.title") }}</p>
          <p class="notif-center__empty-desc">{{ t("notify.empty.desc") }}</p>
        </div>
      </div>

      <footer class="notif-center__foot">
        <RouterLink to="/projects" class="notif-center__link" @click="open = false">
          {{ t("notify.openProjects") }}
          <YmIcon name="chevron-right" :size="13" />
        </RouterLink>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.notif-center {
  position: relative;
}
.notif-center__trigger {
  position: relative;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border-radius: var(--radius-md);
  color: var(--color-text-muted);
  transition: background var(--motion-duration-snap) var(--motion-ease-out), color var(--motion-duration-snap) var(--motion-ease-out);
}
.notif-center__trigger:hover {
  background: var(--color-surface-2);
  color: var(--color-text);
}
.notif-center__badge {
  position: absolute;
  top: -3px;
  right: -5px;
  min-width: 16px;
  height: 16px;
  padding: 0 4px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: var(--radius-pill);
  background: var(--color-primary);
  color: var(--color-on-primary);
  font-size: 10px;
  font-weight: 700;
  line-height: 1;
  box-shadow: 0 0 0 2px var(--color-surface);
}
.notif-center__panel {
  position: absolute;
  top: calc(100% + 10px);
  right: 0;
  z-index: 60;
  display: flex;
  flex-direction: column;
  width: min(360px, calc(100vw - 24px));
  max-height: min(480px, calc(100vh - 96px));
  overflow: hidden;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
  box-shadow: var(--shadow-popover);
  transform-origin: top right;
  animation: ym-pop-in var(--motion-duration-base) var(--motion-ease-out);
}
@keyframes ym-pop-in {
  from {
    opacity: 0;
    transform: translateY(-4px) scale(0.98);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
.notif-center__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  padding: var(--space-4) var(--space-4) var(--space-2);
}
.notif-center__title {
  font-size: 15px;
  font-weight: 600;
}
.notif-center__mark-all {
  color: var(--color-primary);
  font-size: var(--font-size-xs);
}
.notif-center__mark-all:hover {
  text-decoration: underline;
}
.notif-center__filters {
  display: flex;
  gap: var(--space-1);
  padding: 0 var(--space-4) var(--space-2);
}
.notif-center__filter {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 10px;
  border-radius: var(--radius-pill);
  background: var(--color-surface-2);
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
  transition: background var(--motion-duration-snap) var(--motion-ease-out), color var(--motion-duration-snap) var(--motion-ease-out);
}
.notif-center__filter:hover {
  color: var(--color-text);
}
.notif-center__filter.is-active {
  background: var(--color-primary-soft);
  color: var(--color-primary);
  font-weight: 500;
}
.notif-center__filter-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--color-primary);
}
.notif-center__list {
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 2px;
  min-height: 0;
  padding: 0 var(--space-2);
  overflow-y: auto;
}
.notif {
  position: relative;
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
  padding: var(--space-3);
  border-radius: var(--radius-md);
  text-align: left;
  transition: background var(--motion-duration-snap) var(--motion-ease-snap);
}
.notif:hover {
  background: var(--color-surface-2);
}
.notif.is-unread {
  background: var(--color-primary-soft);
}
.notif.is-unread:hover {
  background: color-mix(in srgb, var(--color-primary) 14%, var(--color-surface));
}
.notif__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  flex: none;
  margin-top: 1px;
  border-radius: 8px;
  background: var(--color-surface-3);
  color: var(--color-text-muted);
}
.notif--success .notif__icon {
  background: var(--color-success-soft);
  color: var(--color-success-strong);
}
.notif--error .notif__icon {
  background: var(--color-error-soft);
  color: var(--color-error-strong);
}
.notif--info .notif__icon,
.notif--warning .notif__icon {
  background: var(--color-info-soft);
  color: var(--color-info-strong);
}
.notif__body {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.notif__title {
  color: var(--color-text);
  font-size: var(--font-size-md);
  font-weight: 500;
}
.notif__desc {
  overflow: hidden;
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
  line-height: 1.45;
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}
.notif__time {
  margin-top: 2px;
  color: var(--color-text-faint);
  font-size: 10px;
}
.notif__dot {
  position: absolute;
  top: 50%;
  right: 10px;
  width: 6px;
  height: 6px;
  transform: translateY(-50%);
  border-radius: 50%;
  background: var(--color-primary);
}
.notif-center__empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  padding: 36px 20px;
  text-align: center;
}
.notif-center__empty-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 48px;
  height: 48px;
  border-radius: 14px;
  border: 1px solid var(--color-border);
  background: var(--color-surface-2);
  color: var(--color-primary);
}
.notif-center__empty-title {
  color: var(--color-text-muted);
  font-size: var(--font-size-md);
  font-weight: 500;
}
.notif-center__empty-desc {
  max-width: 240px;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
  line-height: 1.5;
}
.notif-center__foot {
  display: flex;
  justify-content: center;
  padding: var(--space-2);
  border-top: 1px solid var(--color-border);
}
.notif-center__link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  border-radius: var(--radius-md);
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
  text-decoration: none;
}
.notif-center__link:hover {
  color: var(--color-primary);
  text-decoration: none;
}
</style>
