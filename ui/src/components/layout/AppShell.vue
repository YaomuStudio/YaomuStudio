<script setup lang="ts">
import { computed, ref } from "vue";
import { useRoute } from "vue-router";
import TopBar from "./TopBar.vue";
import SidebarNav from "./SidebarNav.vue";
import StatusBar from "./StatusBar.vue";
import UpdateNotification from "../UpdateNotification.vue";

const route = useRoute();
const SIDEBAR_STORAGE_KEY = "yaomu.sidebar.collapsed";

function loadSidebarCollapsed(): boolean {
  try {
    return localStorage.getItem(SIDEBAR_STORAGE_KEY) === "1";
  } catch {
    return false;
  }
}

const sidebarCollapsed = ref(loadSidebarCollapsed());
const immersive = computed(
  () => route.path.startsWith("/manual-correction") || route.path.endsWith("/correction"),
);
const isProjectPreview = computed(() => route.name === "project-detail");

function toggleSidebar(): void {
  sidebarCollapsed.value = !sidebarCollapsed.value;
  try {
    localStorage.setItem(SIDEBAR_STORAGE_KEY, sidebarCollapsed.value ? "1" : "0");
  } catch {
    // 存储不可用时仅影响持久化，不阻塞交互。
  }
}

</script>

<template>
  <div class="app-shell">
    <TopBar
      v-if="!immersive"
      :sidebar-open="!sidebarCollapsed"
      @toggle-sidebar="toggleSidebar"
    />
    <div class="app-shell__body">
      <SidebarNav v-if="!immersive" :collapsed="sidebarCollapsed" />
      <div class="app-shell__right">
        <main class="app-shell__main" :class="{ 'is-project-preview': isProjectPreview }">
          <RouterView />
        </main>
      </div>
    </div>
    <StatusBar />
    <UpdateNotification />
  </div>
</template>

<style scoped>
.app-shell {
  display: flex;
  flex-direction: column;
  height: 100%;
}
.app-shell__body {
  display: flex;
  flex: 1;
  min-height: 0;
}
.app-shell__right {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
}
.app-shell__main {
  flex: 1;
  min-width: 0;
  overflow: auto;
  background:
    radial-gradient(1100px 420px at 50% -200px, var(--color-primary-soft), transparent 72%),
    var(--color-bg);
}
.app-shell__main.is-project-preview {
  background:
    radial-gradient(1100px 420px at 50% -200px, var(--color-primary-soft), transparent 72%),
    var(--color-bg-preview);
}
</style>
