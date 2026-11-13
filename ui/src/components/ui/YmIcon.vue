<script setup lang="ts">
import { computed } from "vue";

const props = withDefaults(defineProps<{ name: string; size?: number }>(), { size: 18 });

// 24×24 stroke 图标（currentColor）。多 path 用数组。
const ICONS: Record<string, string[]> = {
  home: ["M3 11l9-8 9 8", "M5 10v10h14V10"],
  folder: ["M3 6h6l2 2h10v11a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6z"],
  list: ["M8 6h13", "M8 12h13", "M8 18h13", "M3 6h.01", "M3 12h.01", "M3 18h.01"],
  "sidebar-collapse": ["M4 4h16v16H4z", "M9 4v16", "M17.5 9.5L14.5 12l3 2.5"],
  "sidebar-expand": ["M4 4h16v16H4z", "M9 4v16", "M13.5 9.5L16.5 12l-3 2.5"],
  film: ["M4 4h16v16H4z", "M4 9h16", "M4 15h16", "M8 4v16", "M16 4v16"],
  mic: ["M12 3a3 3 0 0 1 3 3v6a3 3 0 0 1-6 0V6a3 3 0 0 1 3-3z", "M5 11a7 7 0 0 0 14 0", "M12 18v3"],
  eraser: ["M5 20h14", "M7 15l9-9 2 2-8 8H7z"],
  layers: ["M12 3l9 5-9 5-9-5 9-5z", "M3 13l9 5 9-5"],
  globe: ["M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z", "M3 12h18", "M12 3c2.2 2.5 2.2 15.5 0 18-2.2-2.5-2.2-15.5 0-18z"],
  subtitles: ["M4 6h16v9H4z", "M7 12h6", "M7 15h4", "M15 12h2", "M15 15h2"],
  link: ["M10 14a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-1.5 1.5", "M14 10a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7L13 18.5"],
  gear: ["M12 8.5a3.5 3.5 0 1 0 0 7 3.5 3.5 0 0 0 0-7z", "M12 2v2.5", "M12 19.5V22", "M4.2 4.2l1.8 1.8", "M18 18l1.8 1.8", "M2 12h2.5", "M19.5 12H22", "M4.2 19.8L6 18", "M18 6l1.8-1.8"],
  play: [
    "M8 6.82c0-.79.87-1.27 1.54-.84l8.14 5.18c.62.39.62 1.29 0 1.68l-8.14 5.18c-.67.43-1.54-.05-1.54-.84V6.82z",
  ],
  "play-round": [
    "M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z M10 8.2v7.6c0 .56.61.9 1.09.61l6.02-3.8c.45-.28.45-.94 0-1.22l-6.02-3.8c-.48-.3-1.09.04-1.09.61z",
  ],
  pause: [
    "M7.4 4.6h2.5c.78 0 1.4.62 1.4 1.4v12c0 .78-.62 1.4-1.4 1.4H7.4c-.78 0-1.4-.62-1.4-1.4V6c0-.78.62-1.4 1.4-1.4z",
    "M14.1 4.6h2.5c.78 0 1.4.62 1.4 1.4v12c0 .78-.62 1.4-1.4 1.4h-2.5c-.78 0-1.4-.62-1.4-1.4V6c0-.78.62-1.4 1.4-1.4z",
  ],
  stop: [
    "M8 6.5h8c.83 0 1.5.67 1.5 1.5v8c0 .83-.67 1.5-1.5 1.5H8c-.83 0-1.5-.67-1.5-1.5V8c0-.83.67-1.5 1.5-1.5z",
  ],
  check: ["M5 13l4 4 10-11"],
  x: ["M6 6l12 12", "M18 6L6 18"],
  "chevron-down": ["M6 9l6 6 6-6"],
  "chevron-up": ["M6 15l6-6 6 6"],
  "chevron-right": ["M9 6l6 6-6 6"],
  "chevron-left": ["M15 6l-6 6 6 6"],
  plus: ["M12 5v14", "M5 12h14"],
  refresh: ["M21 12a9 9 0 1 1-2.6-6.3", "M21 3v6h-6"],
  undo: ["M9 14L4 9l5-5", "M4 9h10a6 6 0 0 1 0 12h-3"],
  redo: ["M15 14l5-5-5-5", "M20 9H10a6 6 0 0 0 0 12h3"],
  help: ["M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z", "M9.5 9.2a2.6 2.6 0 0 1 5 1c0 1.4-1.3 1.9-2.1 2.6-.3.3-.4.7-.4 1.2", "M12 17h.01"],
  edit: ["M12 20h9", "M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4L16.5 3.5z"],
  trash: ["M3 6h18", "M8 6V4h8v2", "M6 6l1 14h10l1-14"],
  download: ["M12 3v12", "M7 10l5 5 5-5", "M4 21h16"],
  upload: ["M12 21V9", "M7 14l5-5 5 5", "M4 3h16"],
  "folder-open": ["M3 6h6l2 2h10v11a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6z", "M4 9h13l1.5 7H5.5"],
  search: ["M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14z", "M21 21l-4-4"],
  info: ["M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z", "M12 8h.01", "M12 12v5"],
  alert: ["M12 3l10 18H2L12 3z", "M12 10v4", "M12 17h.01"],
  volume: ["M4 9v6h4l5 4V5L8 9H4z", "M16 9a4 4 0 0 1 0 6"],
  wave: ["M3 12h2l2-6 3 12 3-9 2 3h6"],
  external: ["M14 4h6v6", "M20 4L10 14", "M20 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1h5"],
  more: ["M5 12h.01", "M12 12h.01", "M19 12h.01"],
  box: ["M12 2l8 4v12l-8 4-8-4V6l8-4z", "M12 22V12", "M12 12l8-4", "M12 12L4 8"],
  shield: ["M12 3l8 3v6c0 4-3 7-8 9-5-2-8-5-8-9V6l8-3z"],
  cpu: ["M7 7h10v10H7z", "M12 7v2", "M12 15v2", "M7 12H5", "M19 12h-2", "M10 3v2", "M14 3v2", "M10 19v2", "M14 19v2"],
  disk: ["M4 4h16v16H4z", "M4 13h16", "M8 8h8"],
  bell: ["M6 9a6 6 0 0 1 12 0c0 5 2 6 2 6H4s2-1 2-6z", "M10 20a2 2 0 0 0 4 0"],
  moon: ["M21 13A8 8 0 1 1 11 3a6.5 6.5 0 0 0 10 10z"],
  sun: ["M12 4a8 8 0 1 0 0 16 8 8 0 0 0 0-16z", "M12 1v2", "M12 21v2", "M4 4l1.5 1.5", "M18.5 18.5L20 20", "M1 12h2", "M21 12h2", "M4 20l1.5-1.5", "M18.5 5.5L20 4"],
};

const FILLED_ICONS = new Set(["play", "play-round", "pause", "stop"]);

const paths = computed(() => ICONS[props.name] ?? ICONS.info);
const filled = computed(() => FILLED_ICONS.has(props.name));
</script>

<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    :fill="filled ? 'currentColor' : 'none'"
    :stroke="filled ? 'none' : 'currentColor'"
    :stroke-width="filled ? undefined : 1.8"
    :stroke-linecap="filled ? undefined : 'round'"
    :stroke-linejoin="filled ? undefined : 'round'"
    :fill-rule="filled ? 'evenodd' : undefined"
    :data-icon="name"
    aria-hidden="true"
  >
    <path v-for="(d, i) in paths" :key="i" :d="d" />
  </svg>
</template>
