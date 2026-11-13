<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";

const props = withDefaults(
  defineProps<{
    label: string;
    disabled?: boolean;
    placement?: "right" | "left" | "bottom" | "top";
    delay?: number;
  }>(),
  { disabled: false, placement: "right", delay: 120 },
);

const triggerEl = ref<HTMLElement | null>(null);
const tooltipEl = ref<HTMLElement | null>(null);
const open = ref(false);
const side = ref<"right" | "left" | "bottom" | "top">("right");
const coords = ref({ left: 0, top: 0 });

let openTimer: number | undefined;
let listening = false;

function clearOpenTimer(): void {
  if (openTimer !== undefined) {
    window.clearTimeout(openTimer);
    openTimer = undefined;
  }
}

/** 根节点可能被设置 CSS zoom；getBoundingClientRect 返回缩放后的视觉坐标，需换算回 CSS px。 */
function rootZoom(): number {
  const raw = getComputedStyle(document.documentElement).zoom;
  const value = Number.parseFloat(raw);
  return Number.isFinite(value) && value > 0 ? value : 1;
}

function computePosition(): void {
  const trigger = triggerEl.value;
  const tooltip = tooltipEl.value;
  if (!trigger || !tooltip) return;

  const zoom = rootZoom();
  const rect = trigger.getBoundingClientRect();
  const tip = tooltip.getBoundingClientRect();
  const gap = 8;
  const margin = 8;

  // 统一换算成 CSS px（zoom 坐标系），供 fixed 定位使用。
  const viewportWidth = window.innerWidth / zoom;
  const viewportHeight = window.innerHeight / zoom;
  const tipWidth = tip.width / zoom;
  const tipHeight = tip.height / zoom;
  const triggerLeft = rect.left / zoom;
  const triggerRight = rect.right / zoom;
  const triggerTop = rect.top / zoom;
  const triggerBottom = rect.bottom / zoom;
  const triggerHeight = rect.height / zoom;
  const triggerWidth = rect.width / zoom;

  if (props.placement === "bottom" || props.placement === "top") {
    const verticalGap = 2;
    const centerX = triggerLeft + triggerWidth / 2;
    const left = Math.min(
      Math.max(centerX, margin + tipWidth / 2),
      viewportWidth - margin - tipWidth / 2,
    );
    let nextSide: "bottom" | "top" = props.placement;
    let top = nextSide === "bottom" ? triggerBottom + verticalGap : triggerTop - verticalGap;
    if (nextSide === "bottom" && top + tipHeight > viewportHeight - margin) {
      nextSide = "top";
      top = triggerTop - verticalGap;
    } else if (nextSide === "top" && top - tipHeight < margin) {
      nextSide = "bottom";
      top = triggerBottom + verticalGap;
    }
    side.value = nextSide;
    coords.value = { left, top };
    return;
  }

  let nextSide: "right" | "left" = props.placement;
  if (nextSide === "right" && triggerRight + gap + tipWidth > viewportWidth - margin) {
    nextSide = "left";
  } else if (nextSide === "left" && triggerLeft - gap - tipWidth < margin) {
    nextSide = "right";
  }

  let left =
    nextSide === "right" ? triggerRight + gap : triggerLeft - gap;
  if (nextSide === "right") {
    left = Math.min(left, viewportWidth - margin - tipWidth);
  } else {
    left = Math.max(left, margin + tipWidth);
  }

  const center = triggerTop + triggerHeight / 2;
  const top = Math.min(
    Math.max(center, margin + tipHeight / 2),
    viewportHeight - margin - tipHeight / 2,
  );

  side.value = nextSide;
  coords.value = { left, top };
}

function addListeners(): void {
  if (listening) return;
  listening = true;
  document.addEventListener("keydown", onKeydown);
  document.addEventListener("scroll", onScroll, true);
  window.addEventListener("resize", hide);
}

function removeListeners(): void {
  if (!listening) return;
  listening = false;
  document.removeEventListener("keydown", onKeydown);
  document.removeEventListener("scroll", onScroll, true);
  window.removeEventListener("resize", hide);
}

function showNow(): void {
  open.value = true;
  void nextTick(computePosition);
  addListeners();
}

function show(immediate = false): void {
  if (props.disabled || !props.label) return;
  clearOpenTimer();
  if (immediate) {
    showNow();
    return;
  }
  openTimer = window.setTimeout(showNow, props.delay);
}

function hide(): void {
  clearOpenTimer();
  open.value = false;
  removeListeners();
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape") hide();
}

function onScroll(): void {
  hide();
}

watch(
  () => props.disabled,
  (disabled) => {
    if (disabled) hide();
  },
);

onBeforeUnmount(() => {
  clearOpenTimer();
  removeListeners();
});

const tooltipStyle = computed(() => {
  const transforms: Record<typeof side.value, string> = {
    right: "translateY(-50%)",
    left: "translate(-100%, -50%)",
    bottom: "translateX(-50%)",
    top: "translate(-50%, -100%)",
  };
  return {
    left: `${coords.value.left}px`,
    top: `${coords.value.top}px`,
    transform: transforms[side.value],
  };
});
</script>

<template>
  <span
    ref="triggerEl"
    class="ym-tooltip__trigger"
    @pointerenter="show()"
    @pointerleave="hide"
    @focusin="show(true)"
    @focusout="hide"
  >
    <slot />
  </span>
  <Teleport to="body">
    <div
      v-if="open && !disabled"
      ref="tooltipEl"
      class="ym-tooltip"
      role="tooltip"
      :style="tooltipStyle"
    >
      {{ label }}
    </div>
  </Teleport>
</template>

<style scoped>
.ym-tooltip__trigger {
  display: inline-flex;
}
.ym-tooltip {
  position: fixed;
  z-index: 950;
  width: max-content;
  max-width: 340px;
  padding: 5px 8px;
  border-radius: var(--radius-sm);
  background: var(--color-text);
  color: var(--color-bg);
  font-size: var(--font-size-xs);
  line-height: var(--line-height-normal);
  box-shadow: var(--shadow-popover);
  pointer-events: none;
  animation: ym-tooltip-in var(--motion-duration-snap) var(--motion-ease-out);
}
@keyframes ym-tooltip-in {
  from {
    opacity: 0;
    translate: 0 2px;
  }
  to {
    opacity: 1;
    translate: 0 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  .ym-tooltip {
    animation: none;
  }
}
</style>
