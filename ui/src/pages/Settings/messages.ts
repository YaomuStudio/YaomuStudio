import type { AreaMessages } from "@/i18n";

const messages: AreaMessages = {
  en: {
    // General
    "settings.general.title": "General",
    "settings.general.desc": "Startup, closing behavior, and update checks.",
    "settings.general.startup": "Open at launch",
    "settings.general.startupHint": "Choose what the app shows when it starts.",
    "settings.general.startup.workbench": "Workbench",
    "settings.general.startup.lastProject": "Last project",
    "settings.general.close": "Closing the window",
    "settings.general.closeHint": "Minimizing keeps the application available from the tray.",
    "settings.general.close.quit": "Quit the app",
    "settings.general.close.minimize": "Minimize to tray",
    "settings.general.updates": "Automatic update checks",
    "settings.general.updatesHint":
      "Checks for a new version at launch. Updates always require confirmation and never interrupt running tasks.",

    // Language
    "settings.language.title": "Language",
    "settings.language.desc": "Interface language and translation coverage. Changes apply immediately.",
    "settings.language.followSystem": "Follow system language",
    "settings.language.followSystemHint":
      "Uses the closest matching interface language. Languages without a pack fall back to English.",
    "settings.language.systemDetected": "System language detected: {language}",
    "settings.language.current": "Current interface language",
    "settings.language.completion": "{percent}% translated",
    "settings.language.packMissing":
      "No language pack yet. The interface falls back to English.",
    "settings.language.packPartial":
      "Only part of this language is translated. Missing text falls back to English.",
    "settings.language.listNote":
      "All 26 supported interface languages are listed by their native names. Completion is measured from the installed language packs; Simplified Chinese and English are complete, and other languages are partial with English fallback.",
    "settings.language.rtl": "Right-to-left layout",

    // Appearance
    "settings.appearance.title": "Appearance",
    "settings.appearance.desc": "Theme and interface scale. Changes apply immediately.",
    "settings.appearance.theme": "Theme",
    "settings.appearance.themeHint": "Choose light, dark, or follow the system.",
    "settings.appearance.theme.light": "Light",
    "settings.appearance.theme.dark": "Dark",
    "settings.appearance.theme.system": "Follow system",
    "settings.appearance.scale": "Interface scale",
    "settings.appearance.scaleHint":
      "Scale the interface from 80% to 150%. The new scale applies when you release the slider.",
    "settings.appearance.reset": "Reset to 100%",
    "settings.appearance.preview": "Preview",
    "settings.appearance.previewTitle": "Local production, no cloud",
    "settings.appearance.previewBody": "This text follows the interface scale and theme settings.",
    "settings.appearance.previewHint":
      "The preview uses the selected theme and interface scale.",

    // Storage
    "settings.storage.title": "Storage",
    "settings.storage.desc": "Disk usage by category, cleanup, and the model package location.",
    "settings.storage.total": "Total used",
    "settings.storage.models": "Model packages",
    "settings.storage.media": "Media library",
    "settings.storage.cache": "Cache",
    "settings.storage.exports": "Exports",
    "settings.storage.clean": "Clean",
    "settings.storage.cleanTitle": "Clean {category}",
    "settings.storage.cleanBody":
      "Removes execution logs, temporary caches, and unreferenced model packages. Project data, exports, and active/current models are kept.",
    "settings.storage.cleanDone": "Freed {bytes}.",
    "settings.storage.cleanFailed": "Cleanup failed. Check active tasks and try again.",
    "settings.storage.refresh": "Refresh usage",
    "settings.storage.locationTitle": "Model package location",
    "settings.storage.locationHint":
      "This version stores model packages in the default application data folder. Custom locations are not available yet.",
    "settings.storage.locationPlaceholder": "Default application data folder",
    "settings.storage.browse": "Choose folder",
    "settings.storage.validate": "Validate and migrate",
    "settings.storage.migrationPlaceholder":
      "Choosing a directory and migrating model packages are not available in this version.",
    "settings.storage.diskHint":
      "Keep at least 30 GB free on the application data partition for all model packages plus working cache.",
    "settings.storage.advancedTitle": "Advanced cleanup",
    "settings.storage.advancedDesc":
      "Remove app-owned runtime and caches only. Project data and other WSL distributions are never touched here.",
    "settings.storage.runtimeTitle": "WSL runtime environment",
    "settings.storage.runtimeDesc":
      "The app-owned WSL distribution and its model cache. Cleaning stops the local engine; restart the app afterwards. This can be several GB and will require rebuilding the runtime later.",
    "settings.storage.runtimeClean": "Clean runtime",
    "settings.storage.runtimeConfirmTitle": "Clean WSL runtime?",
    "settings.storage.runtimeConfirmBody":
      "This unregisters the app-owned yaomu WSL distribution and removes its cache. The local engine will stop, and you must restart the app. Project data, exported files, and other WSL distributions are not touched.",
    "settings.storage.runtimeConfirm": "Clean and stop engine",
    "settings.storage.runtimeBusy":
      "Active tasks are still running. Stop them before cleaning the runtime.",
    "settings.storage.runtimeCleanupFailed":
      "Could not clean the runtime. The local engine has stopped; restart the app and try again.",
    "settings.storage.runtimeRemoved": "WSL runtime cleaned. Restart the app to continue.",
    "settings.storage.runtimeNotInstalled": "The app WSL runtime is not installed.",
    "settings.storage.runtimeUnsupported": "Runtime cleanup is available on Windows only.",
    "settings.storage.share": "{percent} of total",

    // Devices
    "settings.devices.title": "Compute devices",
    "settings.devices.desc": "Detected accelerators and the device used for model execution.",
    "settings.devices.auto": "Auto (recommended)",
    "settings.devices.autoHint": "Automatically selects MPS, CUDA, or CPU.",
    "settings.devices.selection": "Device selection",
    "settings.devices.recommended": "Recommended",
    "settings.devices.inUse": "In use",
    "settings.devices.vram": "Memory",
    "settings.devices.driver": "Driver",
    "settings.devices.kind.cpu": "CPU",
    "settings.devices.kind.cuda": "CUDA",
    "settings.devices.kind.mps": "Apple MPS",
    "settings.devices.current": "Current device",
    "settings.devices.currentValue": "{name} ({kind})",
    "settings.devices.currentAuto": "Auto → {name}",
    "settings.devices.loadStatus": "Model load status",
    "settings.devices.loadValue": "{running} of {total} models running",
    "settings.devices.loadHint":
      "Running means a task is executing; the model is loaded or loading and is released when the task ends.",
    "settings.devices.usage": "Memory usage",
    "settings.devices.usageHint":
      "Device-level memory usage; the exact metric depends on the operating system.",
    "settings.devices.runtimeTitle": "Model runtime",
    "settings.devices.runtimeIdle": "Idle",
    "settings.devices.runtimeRunning": "Running",
    "settings.devices.runtimeTaskCount": "{count} active",
    "settings.devices.runtimeHint":
      "Status reflects execution occupancy, not resident model weights.",
    "settings.devices.switchTitle": "Switch compute device?",
    "settings.devices.switchBody":
      "Switching devices interrupts running tasks. Affected tasks will be re-queued on the new device.",
    "settings.devices.switchConfirm": "Switch device",
    "settings.devices.refresh": "Detect again",
    "settings.devices.noDevices": "No compute devices detected.",
    "settings.devices.deviceList": "Detected devices",
    "settings.devices.switched": "Compute device updated for models.",
    "settings.devices.switchFailed": "Could not switch the compute device: {error}",

    // Privacy
    "settings.privacy.title": "Privacy",
    "settings.privacy.desc": "What stays on this device and what is optional.",
    "settings.privacy.localTitle": "Media and voiceprints never leave the device",
    "settings.privacy.localBody":
      "Imported media, subtitles, and cloned voice data are processed locally. No cloud upload happens unless a feature is explicitly configured to do so.",
    "settings.privacy.consentTitle": "Use only voice samples you have the right to use",
    "settings.privacy.consentBody":
      "The app does not record per-task authorization confirmations. You are responsible for ensuring you have permission to use a voice sample.",
    "settings.privacy.telemetryTitle": "Anonymous usage statistics",
    "settings.privacy.telemetryBody":
      "Off by default. When enabled, only feature counts are sent — never media, subtitle text, voiceprints, file paths, or tokens.",
    "settings.privacy.telemetryStatusOff": "Currently off",
    "settings.privacy.telemetryStatusOn": "Currently on",
    "settings.privacy.telemetryScopeTitle": "What is collected",
    "settings.privacy.telemetryScope1":
      "Feature usage counters, such as a pipeline being started or a model being downloaded.",
    "settings.privacy.telemetryScope2": "App version, platform family, and interface locale.",
    "settings.privacy.telemetryScope3":
      "Never: media content, subtitle text, voice samples, file names, environment variables, or credentials.",
    "settings.privacy.telemetryEnable": "Enable statistics",
    "settings.privacy.logsTitle": "Local logs",
    "settings.privacy.logsBody":
      "Logs are always recorded locally and do not include media or subtitle content, keys, or full environment variables.",

    // Local API
    "settings.localApi.title": "Local API",
    "settings.localApi.desc":
      "Provides an OpenAI-compatible interface on this machine for third-party tools.",
    "settings.localApi.enable": "Enable local API",
    "settings.localApi.enableHint":
      "Off by default. Binding is limited to 127.0.0.1 and requires a bearer token.",
    "settings.localApi.enableTitle": "Enable the local API?",
    "settings.localApi.enableBody":
      "The service listens on 127.0.0.1 only and rejects non-local connections. A bearer token is generated and required for every request. API calls share the same queue as interface tasks.",
    "settings.localApi.enableConfirm": "Enable and generate token",
    "settings.localApi.binding": "Loopback binding",
    "settings.localApi.bindingValue": "127.0.0.1 only",
    "settings.localApi.token": "Access token",
    "settings.localApi.tokenHint":
      "Bearer token required by third-party requests. Tokens can be rotated or revoked at any time.",
    "settings.localApi.tokenPlaceholder": "Generated when the API is enabled",
    "settings.localApi.rotate": "Rotate token",
    "settings.localApi.revoke": "Revoke and disable",
    "settings.localApi.revokeTitle": "Revoke the access token?",
    "settings.localApi.revokeBody":
      "All third-party requests will fail immediately and the local API stops listening until it is enabled again.",
    "settings.localApi.rotated": "A new token was issued. Previous clients must update their credentials.",
    "settings.localApi.revoked": "The local API is disabled and the token was revoked.",
    "settings.localApi.tokenPlaceholderNote":
      "The plain token is shown only once, right after issuance. It is never written to logs, URLs, or local storage.",
    "settings.localApi.statsTitle": "Recent calls",
    "settings.localApi.totalCalls": "Total calls",
    "settings.localApi.endpoint": "API path",
    "settings.localApi.calls": "Calls",
    "settings.localApi.lastError": "Last error",
    "settings.localApi.noCalls": "No calls recorded yet.",
    "settings.localApi.statsHint":
      "Call records contain API path names and counts only. Media content and request bodies are never logged.",
    "settings.localApi.concurrency": "API concurrency limit",
    "settings.localApi.concurrencyHint":
      "Limits how many API jobs can run alongside interface tasks. It only applies to newly accepted third-party requests.",
    "settings.localApi.unlimited": "Unlimited",
    "settings.localApi.refresh": "Refresh statistics",
    "settings.localApi.disabledHint": "Enable the local API to see call statistics.",
    "settings.localApi.error": "Could not load local API settings: {error}",
    "settings.localApi.enabled": "The local API is enabled.",
    "settings.localApi.concurrencySaved": "Concurrency limit updated.",

    // Notifications
    "settings.notifications.title": "Notifications",
    "settings.notifications.desc":
      "Choose which events produce system notifications. In-app status is always shown.",
    "settings.notifications.taskDone": "Project completed",
    "settings.notifications.taskDoneHint": "When a project finishes successfully.",
    "settings.notifications.taskFailed": "Production failed",
    "settings.notifications.taskFailedHint": "When a project's production fails and needs attention.",
    "settings.notifications.downloadDone": "Model download completed",
    "settings.notifications.downloadDoneHint":
      "When a model package finishes downloading and verification.",
    "settings.notifications.updateAvailable": "Update available",
    "settings.notifications.updateAvailableHint": "When a new application version is available.",
    "settings.notifications.permissionNote":
      "System notifications require operating system permission. If notifications are blocked, they still appear in the notification center.",

    // About
    "settings.about.title": "About",
    "settings.about.desc": "Version, licenses, updates, and diagnostics.",
    "settings.about.version": "Version",
    "settings.about.platform": "Platform",
    "settings.about.license": "Licenses",
    "settings.about.licenseClient": "Client application",
    "settings.about.licenseClientValue": "Apache-2.0",
    "settings.about.licenseModels": "Model packages",
    "settings.about.licenseModelsBadge": "Per bundle",
    "settings.about.licenseModelsValue":
      "Each model package declares its own license in its model information. No model package is bundled with the client.",
    "settings.about.repo": "Source repository",
    "settings.about.repoPending": "Repository URL will be published with the open-source release.",
    "settings.about.updates": "Updates",
    "settings.about.autoCheck": "Automatic update checks",
    "settings.about.autoCheckHint":
      "Check on startup, every 4 hours, and when the window regains focus. Updates never interrupt running work.",
    "settings.about.autoDownload": "Download updates automatically",
    "settings.about.autoDownloadHint":
      "Download in the background when an update is found. Installing still requires your confirmation.",
    "settings.about.autoDownloadOutdated":
      "Restart the app to enable automatic downloads with this version.",
    "settings.about.check": "Check for updates",
    "settings.about.phase.idle": "No update check has been run in this session.",
    "settings.about.phase.checking": "Checking for updates…",
    "settings.about.phase.available": "Version {version} is available.",
    "settings.about.phase.downloading": "Downloading version {version}… {percent}%",
    "settings.about.phase.downloaded": "Version {version} is ready to install.",
    "settings.about.phase.error": "Update failed. See the application log for details.",
    "settings.about.download": "Download update",
    "settings.about.install": "Restart and install",
    "settings.about.install.cancel": "Cancel installation",
    "settings.about.install.waiting": "Waiting for running work before installing version {version}…",
    "settings.about.install.waitingActive": "Waiting for active work to finish before installing version {version}…",
    "settings.about.install.waitingRequests": "Waiting for in-flight requests to finish before installing version {version}…",
    "settings.about.install.waitingExecutor": "Waiting for the execution engine to start before installing version {version}…",
    "settings.about.install.preparing": "Preparing version {version} for installation. This step cannot be canceled.",
    "settings.about.install.stopping": "Stopping the local engine before installation…",
    "settings.about.install.installing": "Installing version {version}…",
    "settings.about.install.blocked.runtimeUnavailable": "The local engine is unavailable, so installation cannot continue.",
    "settings.about.install.blocked.runtimeChanged": "The local engine identity changed. Request the update again.",
    "settings.about.install.blocked.executorUnconfirmed": "Execution resources could not be confirmed closed, so installation is blocked.",
    "settings.about.install.blocked.prepareFailed": "Update preparation failed. No installation was started.",
    "settings.about.install.blocked.stopUnconfirmed": "The local engine stop could not be confirmed. Installation is blocked.",
    "settings.about.install.blocked.restartRequired": "The app must be restarted before it can install an update.",
    "settings.about.install.blocked.updateUnavailable": "No downloaded update is available to install.",
    "settings.about.install.blocked.installFailed": "Update installation failed. Restart the app before retrying.",
    "settings.about.install.restartHint": "Restart the app, then check for updates again. The update will not take effect until you restart.",
    "settings.about.install.bridgeOutdated": "This window is using an older update component. Restart the app before installing updates.",
    "settings.about.install.unsupported": "Automatic update installation is not available in this environment.",
    "settings.about.install.denied": "This window is not allowed to start an update installation.",
    "settings.about.install.failed": "The update installation request failed. No installer was started.",
    "settings.about.bridgeUnavailable": "Updates cannot be installed in this environment. Use the desktop app.",
    "settings.about.diagnostics": "Diagnostics",
    "settings.about.diagnosticsBody":
      "Exports a redacted report with the app version, platform, locale, theme, capability states, and storage totals. Media, subtitles, tokens, file names, and environment variables are excluded.",
    "settings.about.export": "Export diagnostics",
    "settings.about.exported": "Diagnostics report exported.",
    "settings.about.copyright": "Copyright © 2026 aodianyun. Licensed under Apache-2.0.",
  },
  zhCN: {
    // General
    "settings.general.title": "通用",
    "settings.general.desc": "启动、关闭行为与更新检查。",
    "settings.general.startup": "启动时打开",
    "settings.general.startupHint": "选择应用启动时显示的页面。",
    "settings.general.startup.workbench": "工作台",
    "settings.general.startup.lastProject": "上次项目",
    "settings.general.close": "关闭窗口时",
    "settings.general.closeHint": "最小化后仍可从托盘使用应用。",
    "settings.general.close.quit": "退出应用",
    "settings.general.close.minimize": "最小化到托盘",
    "settings.general.updates": "自动检查更新",
    "settings.general.updatesHint": "启动时检查新版本。更新始终需要确认，且不会打断运行中的任务。",

    // Language
    "settings.language.title": "语言",
    "settings.language.desc": "界面语言与翻译完整度。切换即时生效。",
    "settings.language.followSystem": "跟随系统语言",
    "settings.language.followSystemHint":
      "就近匹配界面语言；没有语言包的语言回退英文。",
    "settings.language.systemDetected": "检测到系统语言：{language}",
    "settings.language.current": "当前界面语言",
    "settings.language.completion": "已翻译 {percent}%",
    "settings.language.packMissing": "尚无该语言包，界面已回退英文。",
    "settings.language.packPartial": "该语言仅完成部分翻译，缺失文案回退英文。",
    "settings.language.listNote":
      "全部 26 种界面语言均以母语名称列示。完成度按已安装语言包的实际键数计算；简体中文与 English 完整覆盖，其余语言为部分翻译，缺键回退英文。",
    "settings.language.rtl": "从右到左布局",

    // Appearance
    "settings.appearance.title": "外观",
    "settings.appearance.desc": "主题与界面缩放。更改即时生效。",
    "settings.appearance.theme": "主题",
    "settings.appearance.themeHint": "可选择浅色、深色或跟随系统。",
    "settings.appearance.theme.light": "浅色",
    "settings.appearance.theme.dark": "深色",
    "settings.appearance.theme.system": "跟随系统",
    "settings.appearance.scale": "界面缩放",
    "settings.appearance.scaleHint": "在 80%–150% 之间缩放界面，松开滑块后生效。",
    "settings.appearance.reset": "恢复 100%",
    "settings.appearance.preview": "预览",
    "settings.appearance.previewTitle": "本地生产，不经过云端",
    "settings.appearance.previewBody": "此文本随界面缩放和主题设置变化。",
    "settings.appearance.previewHint": "预览会使用所选主题与界面缩放。",

    // Storage
    "settings.storage.title": "存储",
    "settings.storage.desc": "按类别查看磁盘用量、清理与模型包位置。",
    "settings.storage.total": "总占用",
    "settings.storage.models": "模型包",
    "settings.storage.media": "媒体库",
    "settings.storage.cache": "缓存",
    "settings.storage.exports": "导出",
    "settings.storage.clean": "清理",
    "settings.storage.cleanTitle": "清理{category}",
    "settings.storage.cleanBody": "清理执行日志、临时缓存和未被引用的模型包，不删除项目数据、导出或当前模型。",
    "settings.storage.cleanDone": "已释放 {bytes}。",
    "settings.storage.cleanFailed": "清理失败，请检查是否有运行中的任务后重试。",
    "settings.storage.refresh": "刷新用量",
    "settings.storage.locationTitle": "模型包存储位置",
    "settings.storage.locationHint":
      "当前版本模型包固定存放在默认应用数据目录，自定义位置暂未提供。",
    "settings.storage.locationPlaceholder": "默认应用数据目录",
    "settings.storage.browse": "选择目录",
    "settings.storage.validate": "校验并迁移",
    "settings.storage.migrationPlaceholder":
      "当前版本暂不支持选择目录或迁移模型包。",
    "settings.storage.diskHint":
      "建议应用数据所在分区至少保留 30 GB，用于全部模型包与工作缓存。",
    "settings.storage.advancedTitle": "高级清理",
    "settings.storage.advancedDesc":
      "仅清理应用自有的运行环境与缓存，不触碰项目数据和其他 WSL 发行版。",
    "settings.storage.runtimeTitle": "WSL 运行环境",
    "settings.storage.runtimeDesc":
      "应用自有的 WSL 发行版及其模型缓存。清理会停止本地引擎，完成后需重启应用；可能释放数 GB，但下次使用需重新准备运行环境。",
    "settings.storage.runtimeClean": "清理运行环境",
    "settings.storage.runtimeConfirmTitle": "清理 WSL 运行环境？",
    "settings.storage.runtimeConfirmBody":
      "将注销应用自有的 yaomu WSL 发行版并删除其缓存；本地引擎会停止，需要重启应用。项目数据、导出文件和其他 WSL 发行版不会被删除。",
    "settings.storage.runtimeConfirm": "清理并停止引擎",
    "settings.storage.runtimeBusy": "仍有活动任务，请先停止任务再清理运行环境。",
    "settings.storage.runtimeCleanupFailed": "运行环境清理失败，本地引擎已停止；请重启应用后重试。",
    "settings.storage.runtimeRemoved": "WSL 运行环境已清理，请重启应用。",
    "settings.storage.runtimeNotInstalled": "应用 WSL 运行环境未安装。",
    "settings.storage.runtimeUnsupported": "运行环境清理仅支持 Windows。",
    "settings.storage.share": "占总占用 {percent}",

    // Devices
    "settings.devices.title": "计算设备",
    "settings.devices.desc": "已探测的加速器与模型执行所用设备。",
    "settings.devices.auto": "自动（推荐）",
    "settings.devices.autoHint": "自动选择 MPS、CUDA 或 CPU。",
    "settings.devices.selection": "设备选择",
    "settings.devices.recommended": "推荐",
    "settings.devices.inUse": "使用中",
    "settings.devices.vram": "内存/显存",
    "settings.devices.driver": "驱动",
    "settings.devices.kind.cpu": "CPU",
    "settings.devices.kind.cuda": "CUDA",
    "settings.devices.kind.mps": "Apple MPS",
    "settings.devices.current": "当前设备",
    "settings.devices.currentValue": "{name}（{kind}）",
    "settings.devices.currentAuto": "自动 → {name}",
    "settings.devices.loadStatus": "模型载入状态",
    "settings.devices.loadValue": "{total} 个模型中 {running} 个执行中",
    "settings.devices.loadHint":
      "运行中表示该模型有任务正在执行；模型在任务开始时载入，结束后释放。",
    "settings.devices.usage": "内存占用",
    "settings.devices.usageHint": "设备级内存占用；具体统计口径取决于操作系统。",
    "settings.devices.runtimeTitle": "模型运行状态",
    "settings.devices.runtimeIdle": "空闲",
    "settings.devices.runtimeRunning": "运行中",
    "settings.devices.runtimeTaskCount": "{count} 个任务执行中",
    "settings.devices.runtimeHint": "状态来自执行占用，不代表模型权重常驻。",
    "settings.devices.switchTitle": "切换计算设备？",
    "settings.devices.switchBody":
      "切换设备会中断运行中的任务，受影响的任务将在新设备上重新排队。",
    "settings.devices.switchConfirm": "切换设备",
    "settings.devices.refresh": "重新探测",
    "settings.devices.noDevices": "未探测到计算设备。",
    "settings.devices.deviceList": "已探测设备",
    "settings.devices.switched": "已更新模型的计算设备。",
    "settings.devices.switchFailed": "切换计算设备失败：{error}",

    // Privacy
    "settings.privacy.title": "隐私",
    "settings.privacy.desc": "哪些数据留在本机，哪些是可选项。",
    "settings.privacy.localTitle": "素材与声纹不出设备",
    "settings.privacy.localBody":
      "导入的媒体、字幕与克隆声纹均在本机处理。除非某功能被明确配置为联网，否则不会上传云端。",
    "settings.privacy.consentTitle": "仅使用你拥有权利的声音样本",
    "settings.privacy.consentBody":
      "应用不再记录逐次授权确认；请自行确保已获得声音样本的使用权。",
    "settings.privacy.telemetryTitle": "匿名使用统计",
    "settings.privacy.telemetryBody":
      "默认关闭。开启后仅发送功能计数，绝不会包含素材、字幕正文、声纹、文件路径或令牌。",
    "settings.privacy.telemetryStatusOff": "当前关闭",
    "settings.privacy.telemetryStatusOn": "当前开启",
    "settings.privacy.telemetryScopeTitle": "采集范围",
    "settings.privacy.telemetryScope1": "功能使用计数，例如启动了管线或下载了模型。",
    "settings.privacy.telemetryScope2": "应用版本、平台大类与界面语言。",
    "settings.privacy.telemetryScope3":
      "绝不采集：媒体内容、字幕正文、声音样本、文件名、环境变量或凭据。",
    "settings.privacy.telemetryEnable": "开启统计",
    "settings.privacy.logsTitle": "本地日志",
    "settings.privacy.logsBody":
      "日志始终记录在本机，不包含媒体或字幕正文、密钥及完整环境变量。",

    // Local API
    "settings.localApi.title": "本地 API",
    "settings.localApi.desc": "在本机为第三方工具提供 OpenAI 兼容接口。",
    "settings.localApi.enable": "启用本地 API",
    "settings.localApi.enableHint": "默认关闭。仅绑定 127.0.0.1，且要求携带访问令牌。",
    "settings.localApi.enableTitle": "启用本地 API？",
    "settings.localApi.enableBody":
      "服务仅监听 127.0.0.1，拒绝非本机连接。启用时会生成访问令牌，所有请求都必须携带。API 调用与界面任务共用同一队列。",
    "settings.localApi.enableConfirm": "启用并生成令牌",
    "settings.localApi.binding": "回环绑定",
    "settings.localApi.bindingValue": "仅 127.0.0.1",
    "settings.localApi.token": "访问令牌",
    "settings.localApi.tokenHint":
      "第三方请求需携带的 Bearer 令牌。令牌可随时轮换或吊销。",
    "settings.localApi.tokenPlaceholder": "启用 API 后生成",
    "settings.localApi.rotate": "轮换令牌",
    "settings.localApi.revoke": "吊销并关闭",
    "settings.localApi.revokeTitle": "吊销访问令牌？",
    "settings.localApi.revokeBody":
      "所有第三方请求将立即失败，本地 API 在重新启用前停止监听。",
    "settings.localApi.rotated": "已签发新令牌，旧客户端需更新凭据。",
    "settings.localApi.revoked": "本地 API 已关闭，令牌已吊销。",
    "settings.localApi.tokenPlaceholderNote":
      "明文令牌仅在签发时显示一次，不写入日志、URL 或本地存储。",
    "settings.localApi.statsTitle": "近期调用",
    "settings.localApi.totalCalls": "调用总数",
    "settings.localApi.endpoint": "接口地址",
    "settings.localApi.calls": "调用次数",
    "settings.localApi.lastError": "最近错误",
    "settings.localApi.noCalls": "暂无调用记录。",
    "settings.localApi.statsHint":
      "调用记录仅包含接口名称与次数，绝不记录素材内容或请求正文。",
    "settings.localApi.concurrency": "API 并发上限",
    "settings.localApi.concurrencyHint":
      "限制新受理的第三方请求数量，不抢占或中断界面任务。",
    "settings.localApi.unlimited": "不限制",
    "settings.localApi.refresh": "刷新统计",
    "settings.localApi.disabledHint": "启用本地 API 后可查看调用统计。",
    "settings.localApi.error": "无法加载本地 API 设置：{error}",
    "settings.localApi.enabled": "本地 API 已启用。",
    "settings.localApi.concurrencySaved": "并发上限已更新。",

    // Notifications
    "settings.notifications.title": "通知",
    "settings.notifications.desc": "选择哪些事件发送系统通知。应用内状态始终可见。",
    "settings.notifications.taskDone": "项目完成",
    "settings.notifications.taskDoneHint": "项目成功结束时通知。",
    "settings.notifications.taskFailed": "制作失败",
    "settings.notifications.taskFailedHint": "项目制作失败且需要处理时通知。",
    "settings.notifications.downloadDone": "模型下载完成",
    "settings.notifications.downloadDoneHint": "模型包下载并校验完成后通知。",
    "settings.notifications.updateAvailable": "有可用更新",
    "settings.notifications.updateAvailableHint": "发现新应用版本时通知。",
    "settings.notifications.permissionNote":
      "系统通知需要操作系统授权。若通知被屏蔽，应用会在任务中心显示对应状态。",

    // About
    "settings.about.title": "关于",
    "settings.about.desc": "版本、许可证、更新与诊断。",
    "settings.about.version": "版本号",
    "settings.about.platform": "平台",
    "settings.about.license": "许可证",
    "settings.about.licenseClient": "客户端应用",
    "settings.about.licenseClientValue": "Apache-2.0",
    "settings.about.licenseModels": "模型包",
    "settings.about.licenseModelsBadge": "按模型包",
    "settings.about.licenseModelsValue":
      "每个模型包在模型信息中声明各自的许可证。客户端不内置任何模型包。",
    "settings.about.repo": "开源仓库",
    "settings.about.repoPending": "仓库地址将在开源发布时公布。",
    "settings.about.updates": "更新",
    "settings.about.autoCheck": "自动检查更新",
    "settings.about.autoCheckHint":
      "启动时、每 4 小时以及窗口重新获得焦点时检查；更新不会打断运行中的任务。",
    "settings.about.autoDownload": "自动下载更新",
    "settings.about.autoDownloadHint":
      "发现新版本后在后台下载；安装仍需要你确认。",
    "settings.about.autoDownloadOutdated": "重启应用后将启用自动下载。",
    "settings.about.check": "检查更新",
    "settings.about.phase.idle": "本会话尚未检查更新。",
    "settings.about.phase.checking": "正在检查更新…",
    "settings.about.phase.available": "发现新版本 {version}。",
    "settings.about.phase.downloading": "正在下载 {version}… {percent}%",
    "settings.about.phase.downloaded": "版本 {version} 已就绪，可重启安装。",
    "settings.about.phase.error": "更新失败，请查看应用日志。",
    "settings.about.download": "下载更新",
    "settings.about.install": "重启并安装",
    "settings.about.install.cancel": "取消安装",
    "settings.about.install.waiting": "正在等待运行中的工作结束，然后安装版本 {version}…",
    "settings.about.install.waitingActive": "正在等待活动任务结束，然后安装版本 {version}…",
    "settings.about.install.waitingRequests": "正在等待进行中的请求结束，然后安装版本 {version}…",
    "settings.about.install.waitingExecutor": "正在等待执行引擎启动，然后安装版本 {version}…",
    "settings.about.install.preparing": "正在为版本 {version} 准备安装。此步骤不可取消。",
    "settings.about.install.stopping": "正在停止本地引擎，为安装做准备…",
    "settings.about.install.installing": "正在安装版本 {version}…",
    "settings.about.install.blocked.runtimeUnavailable": "本地引擎不可用，安装无法继续。",
    "settings.about.install.blocked.runtimeChanged": "本地引擎身份已变化，请重新发起更新。",
    "settings.about.install.blocked.executorUnconfirmed": "无法确认执行资源已关闭，安装已被阻止。",
    "settings.about.install.blocked.prepareFailed": "更新准备失败，未启动安装。",
    "settings.about.install.blocked.stopUnconfirmed": "无法确认本地引擎已停止，安装已被阻止。",
    "settings.about.install.blocked.restartRequired": "必须重启应用后才能安装更新。",
    "settings.about.install.blocked.updateUnavailable": "没有可安装的已下载更新。",
    "settings.about.install.blocked.installFailed": "更新安装失败，请重启应用后重试。",
    "settings.about.install.restartHint": "请重启应用并重新检查更新；重启前更新不会生效。",
    "settings.about.install.bridgeOutdated": "当前窗口使用的是旧版更新组件，请重启应用后再安装更新。",
    "settings.about.install.unsupported": "当前环境无法自动安装更新。",
    "settings.about.install.denied": "当前窗口无权发起更新安装。",
    "settings.about.install.failed": "更新安装请求失败，未启动安装器。",
    "settings.about.bridgeUnavailable": "当前环境无法安装更新，请使用桌面应用。",
    "settings.about.diagnostics": "诊断",
    "settings.about.diagnosticsBody":
      "导出脱敏报告，包含应用版本、平台、语言、主题、能力状态与存储总量。报告不包含素材、字幕、令牌、文件名及环境变量。",
    "settings.about.export": "导出诊断日志",
    "settings.about.exported": "诊断日志已导出。",
    "settings.about.copyright": "Copyright © 2026 aodianyun。依 Apache-2.0 授权。",
  },
};

export default messages;
