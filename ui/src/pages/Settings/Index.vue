<script setup lang="ts">
import { t } from "@/i18n";
import YmIcon from "@/components/ui/YmIcon.vue";

interface NavItem {
  to: string;
  key: string;
  icon: string;
}

const items: NavItem[] = [
  { to: "/settings/general", key: "settings.nav.general", icon: "gear" },
  { to: "/settings/language", key: "settings.nav.language", icon: "globe" },
  { to: "/settings/appearance", key: "settings.nav.appearance", icon: "moon" },
  { to: "/settings/storage", key: "settings.nav.storage", icon: "disk" },
  { to: "/settings/devices", key: "settings.nav.devices", icon: "cpu" },
  { to: "/settings/models", key: "settings.nav.models", icon: "box" },
  { to: "/settings/privacy", key: "settings.nav.privacy", icon: "shield" },
  { to: "/settings/local-api", key: "settings.nav.localApi", icon: "external" },
  { to: "/settings/notifications", key: "settings.nav.notifications", icon: "bell" },
  { to: "/settings/about", key: "settings.nav.about", icon: "info" },
];
</script>

<template>
  <div class="settings">
    <nav class="settings__nav" :aria-label="t('nav.settings')">
      <p class="settings__title">{{ t("nav.settings") }}</p>
      <RouterLink v-for="i in items" :key="i.to" :to="i.to" class="settings__item">
        <YmIcon :name="i.icon" :size="16" />
        <span>{{ t(i.key) }}</span>
      </RouterLink>
    </nav>
    <section v-scroll-fade class="settings__content ym-scroll-fade">
      <RouterView />
    </section>
  </div>
</template>

<style scoped>
.settings {
  display: flex;
  height: 100%;
  min-height: 0;
}
.settings__nav {
  width: 220px;
  flex: none;
  padding: 20px 12px;
  border-inline-end: 1px solid var(--color-border);
  background: var(--color-surface);
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.settings__title {
  margin-block: 0 8px;
  margin-inline-start: 10px;
  font-size: var(--font-size-xs);
  font-weight: 600;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--color-text-faint);
}
.settings__item {
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
.settings__item:hover {
  background: var(--color-surface-2);
  color: var(--color-text);
}
.settings__item.router-link-active {
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
  font-weight: 600;
}
.settings__content {
  flex: 1;
  min-width: 0;
  overflow: auto;
  padding: 24px var(--page-gutter) var(--page-bottom);
}
.settings__content :deep(.page) {
  margin-inline: auto;
  gap: 20px;
}
</style>
