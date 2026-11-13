<script setup lang="ts">
defineProps<{ kind: "pipeline" | "erase" | "custom" }>();
</script>

<template>
  <!--
    卡片右下角暗纹（面性图标）。统一规范：viewBox 160×120，光学外框约
    x36–130 / y20–108；主体为实心剪影（opacity 0.85）+ evenodd 镂空细节，
    辅助线 0.4、弱化 0.3，三张卡视觉重量一致。
  -->
  <svg
    class="tool-watermark"
    viewBox="0 0 160 120"
    fill="none"
    aria-hidden="true"
    focusable="false"
  >
    <template v-if="kind === 'pipeline'">
      <!-- 播放器（实心圆角屏 + 镂空播放键/字幕条） -->
      <path
        fill-rule="evenodd"
        d="M62 20h44a20 20 0 0 1 20 20v30a20 20 0 0 1-20 20H62a20 20 0 0 1-20-20V40a20 20 0 0 1 20-20z
           M78 40l22 15-22 15V40z
           M70 80h28a3 3 0 0 1 3 3 3 3 0 0 1-3 3H70a3 3 0 0 1-3-3 3 3 0 0 1 3-3z"
        fill="currentColor"
        opacity="0.85"
      />
      <!-- 播放进度 -->
      <circle cx="62" cy="108" r="5.5" fill="currentColor" opacity="0.5" />
      <path d="M74 108h34" stroke="currentColor" stroke-width="6" stroke-linecap="round" opacity="0.3" />
    </template>
    <template v-else-if="kind === 'erase'">
      <!-- 单一主体：大实心橡皮（旋转 + 镂空分隔），下压一条文稿线 -->
      <g transform="rotate(-24 84 70)">
        <path
          fill-rule="evenodd"
          d="M52 48h64a14 14 0 0 1 14 14v16a14 14 0 0 1-14 14H66a14 14 0 0 1-14-14V62a14 14 0 0 1 14-14z
             M74 48h5v44h-5z"
          fill="currentColor"
          opacity="0.85"
        />
      </g>
      <path d="M32 104h66" stroke="currentColor" stroke-width="8" stroke-linecap="round" opacity="0.5" />
    </template>
    <template v-else>
      <!-- 单一主体：三个大实心节点 + 粗连线（链路） -->
      <path
        d="M66 40.6L106 45.4 M107.6 58.2L84.4 85.8 M70.4 80.6L58.6 49.4"
        stroke="currentColor"
        stroke-width="9"
        stroke-linecap="round"
        opacity="0.85"
      />
      <circle cx="56" cy="38" r="17" fill="currentColor" opacity="0.85" />
      <circle cx="116" cy="48" r="17" fill="currentColor" opacity="0.85" />
      <circle cx="78" cy="94" r="17" fill="currentColor" opacity="0.85" />
    </template>
  </svg>
</template>

<style scoped>
.tool-watermark {
  width: 150px;
  height: 112px;
  display: block;
}
</style>
