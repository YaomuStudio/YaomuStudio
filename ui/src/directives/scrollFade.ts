import type { Directive } from "vue";

// 滚动边缘渐隐：按真实可滚动距离写入 mask 变量。
// 不可滚动时保持 0，避免内容没被裁切却永久发虚。
const FADE_SIZE = 28;
const EDGE_TOLERANCE = 2;

interface ScrollFadeState {
  update: () => void;
  cleanup: () => void;
}

const states = new WeakMap<HTMLElement, ScrollFadeState>();

function applyFades(el: HTMLElement): void {
  const max = el.scrollHeight - el.clientHeight;
  if (max <= EDGE_TOLERANCE) {
    el.style.setProperty("--ym-fade-top", "0px");
    el.style.setProperty("--ym-fade-bottom", "0px");
    return;
  }
  const size = Math.min(FADE_SIZE, max);
  el.style.setProperty("--ym-fade-top", el.scrollTop > EDGE_TOLERANCE ? `${size}px` : "0px");
  el.style.setProperty(
    "--ym-fade-bottom",
    max - el.scrollTop > EDGE_TOLERANCE ? `${size}px` : "0px",
  );
}

/** 监听滚动、尺寸与子树变化，更新遮罩变量。
 *  直接读取/写入而不依赖 rAF：后台窗口会暂停 rAF，导致首次挂载不生效。 */
export const scrollFade: Directive<HTMLElement> = {
  mounted(el) {
    const update = (): void => {
      syncChildren();
      applyFades(el);
    };
    const onScroll = (): void => update();
    const resizeObserver = new ResizeObserver(update);
    const mutationObserver = new MutationObserver(update);
    const observedChildren = new Set<Element>();

    // 容器自身尺寸不变时，内容高度仍可能变化；观察直接子元素兜住路由/列表更新。
    const syncChildren = (): void => {
      const next = new Set<Element>(el.children);
      for (const child of next) {
        if (!observedChildren.has(child)) {
          resizeObserver.observe(child);
          observedChildren.add(child);
        }
      }
      for (const child of observedChildren) {
        if (!next.has(child)) {
          resizeObserver.unobserve(child);
          observedChildren.delete(child);
        }
      }
    };

    el.addEventListener("scroll", onScroll, { passive: true });
    resizeObserver.observe(el);
    mutationObserver.observe(el, { childList: true, subtree: true, characterData: true });
    update();
    // 首屏内容/字体常在一次布局后到位，补两次延迟测量。
    const deferred = [window.setTimeout(update, 0), window.setTimeout(update, 120)];

    states.set(el, {
      update,
      cleanup: () => {
        deferred.forEach((id) => window.clearTimeout(id));
        el.removeEventListener("scroll", onScroll);
        resizeObserver.disconnect();
        mutationObserver.disconnect();
      },
    });
  },
  updated(el) {
    states.get(el)?.update();
  },
  unmounted(el) {
    states.get(el)?.cleanup();
    states.delete(el);
  },
};
