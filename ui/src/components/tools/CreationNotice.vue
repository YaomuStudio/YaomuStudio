<script setup lang="ts">
import { t } from "@/i18n";
import YmNotice from "@/components/ui/YmNotice.vue";

defineProps<{ items: string[] }>();
</script>

<template>
  <YmNotice class="creation-notice" tone="info" :title="t('pipeline.new.noticeTitle')">
    <ul class="creation-notice__list">
      <li v-for="item in items" :key="item">{{ item }}</li>
    </ul>
  </YmNotice>
</template>

<style scoped>
.creation-notice__list {
  margin: 0;
  padding-left: 18px;
  line-height: 1.6;
}
</style>
