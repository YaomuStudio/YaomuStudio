// 宿主初始化/就绪界面文案（zh-CN 与 en 100% 覆盖）。
import type { AreaMessages } from "@/i18n";

const messages: AreaMessages = {
  en: {
    "runtime.init.title": "Starting YaomuStudio",
    "runtime.init.body":
      "Preparing the local engine. On Windows, the first launch downloads and imports the Yaomu execution image when it is missing. Media and drafts stay on this device.",
    "runtime.init.status": "Status",
    "runtime.init.language": "Interface language",
    "runtime.init.theme": "Theme",
    "runtime.init.theme.light": "Light",
    "runtime.init.theme.dark": "Dark",
    "runtime.init.theme.system": "System",
    "runtime.retry": "Retry",
    "runtime.phase.preparing": "Preparing the local environment",
    "runtime.phase.starting": "Starting the local service",
    "runtime.phase.ready": "Local service ready",
    "runtime.phase.restarting": "Reconnecting to the local service",
    "runtime.phase.failed": "Startup failed",
    "runtime.phase.stopping": "Stopping local services",
    "runtime.phase.stopped": "Local services stopped",
    "runtime.reason.startup_failed": "The local service could not start.",
    "runtime.reason.core_exited": "The local service exited unexpectedly.",
    "runtime.reason.cleanup_unconfirmed":
      "The previous local process could not be confirmed as stopped. Restart the app to continue.",
    "runtime.reason.handshake_failed":
      "The local service did not pass the readiness check.",
    "runtime.reason.protocol_mismatch":
      "The local service is not compatible with this app version. Restart the app with a matching version.",
    "runtime.reason.workspace_mismatch":
      "The workspace changed. Restart the app before opening another workspace.",
    "runtime.banner.title": "Local services unavailable",
    "runtime.banner.body":
      "State: {phase}. Pages stay as they are and nothing is replayed automatically.",
    "runtime.banner.reason": "Reason: {reason}",
    "runtime.recovery.dirty":
      "Your unsaved changes and the original version were kept. Nothing was saved or submitted automatically; saving again still compares against the original version.",
  },
  zhCN: {
    "runtime.init.title": "正在启动 YaomuStudio",
    "runtime.init.body":
      "正在准备本地引擎。Windows 首次启动时，如果本机还没有 Yaomu 执行镜像，会下载并导入。媒体与草稿只保留在本机。",
    "runtime.init.status": "状态",
    "runtime.init.language": "界面语言",
    "runtime.init.theme": "主题",
    "runtime.init.theme.light": "浅色",
    "runtime.init.theme.dark": "深色",
    "runtime.init.theme.system": "跟随系统",
    "runtime.retry": "重试",
    "runtime.phase.preparing": "正在准备本地环境",
    "runtime.phase.starting": "正在启动本地服务",
    "runtime.phase.ready": "本地服务已就绪",
    "runtime.phase.restarting": "正在重新连接本地服务",
    "runtime.phase.failed": "启动失败",
    "runtime.phase.stopping": "正在停止本地服务",
    "runtime.phase.stopped": "本地服务已停止",
    "runtime.reason.startup_failed": "本地服务无法启动。",
    "runtime.reason.core_exited": "本地服务意外退出。",
    "runtime.reason.cleanup_unconfirmed": "无法确认上一个本地进程已停止，请重启应用后继续。",
    "runtime.reason.handshake_failed": "本地服务未通过就绪检查。",
    "runtime.reason.protocol_mismatch": "本地服务与应用版本不兼容，请使用匹配版本重启应用。",
    "runtime.reason.workspace_mismatch": "工作区已变化，请重启应用后再打开其他工作区。",
    "runtime.banner.title": "本地服务暂不可用",
    "runtime.banner.body": "当前状态：{phase}。页面保持不变，不会自动重放任何操作。",
    "runtime.banner.reason": "原因：{reason}",
    "runtime.recovery.dirty":
      "已保留未保存的修改和原始版本；不会自动保存或提交，重新保存仍会与原始版本比对。",
  },
};

export default messages;
