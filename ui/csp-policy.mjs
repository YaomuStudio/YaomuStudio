/**
 * 生产构建 CSP：只允许本地应用资源与 yaomu-asset 播放协议。
 *
 * - 由 Vite `build` 阶段 transformIndexHtml 注入 meta；`serve` 不注入，保留 HMR。
 * - 只看 command，不受 NODE_ENV / import.meta.env.DEV 影响。
 * - 不使用 * 或 http(s) 远程源；script 禁止 inline/eval；style 的 unsafe-inline
 *   仅用于 Vue 运行时动态样式。
 * - frame-ancestors 等仅 header 生效的指令不放进 meta，避免无效告警。
 */

export const PRODUCTION_CSP = [
  "default-src 'none'",
  "script-src 'self'",
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data: blob: yaomu-asset:",
  "media-src 'self' blob: yaomu-asset:",
  "connect-src 'self' yaomu-asset:",
  "font-src 'self' data:",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
  "frame-src 'none'",
].join("; ");

/** 仅生产构建返回 CSP meta 标签；开发 serve 返回空数组以保留 HMR。 */
export function productionCspPlugin(command) {
  return {
    name: "yaomu-production-csp",
    transformIndexHtml() {
      if (command !== "build") return [];
      return [{
        tag: "meta",
        attrs: { "http-equiv": "Content-Security-Policy", content: PRODUCTION_CSP },
        injectTo: "head-prepend",
      }];
    },
  };
}
