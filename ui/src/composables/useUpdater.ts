import { computed, ref } from "vue";
import { t } from "@/i18n";
import { useNotificationsStore } from "@/stores/notifications";
import { useSettingsStore } from "@/stores/settings";

export type UpdaterPhase =
  | "idle"
  | "checking"
  | "available"
  | "downloading"
  | "downloaded"
  | "error";

export type UpdateInstallPhase =
  | "idle"
  | "waiting"
  | "preparing"
  | "stopping"
  | "installing"
  | "blocked";

export type UpdateInstallReason =
  | "active_work"
  | "request_in_flight"
  | "executor_starting"
  | "runtime_unavailable"
  | "runtime_changed"
  | "executor_unconfirmed"
  | "prepare_failed"
  | "stop_unconfirmed"
  | "restart_required"
  | "update_unavailable"
  | "install_failed";

/** 宿主安装状态（contracts/app-update-installation.schema.json 的 Renderer 只读投影）。 */
export interface UpdateInstallSnapshot {
  schemaVersion: 1;
  revision: number;
  phase: UpdateInstallPhase;
  reason: UpdateInstallReason | null;
  version: string | null;
  canCancel: boolean;
}

export type InstallActionError = "unavailable" | "denied" | "failed" | null;

// 模块级单例：自动更新状态是全应用唯一的，避免多处 useUpdater 重复订阅。
const phase = ref<UpdaterPhase>("idle");
const version = ref("");
const releaseNotes = ref("");
const progress = ref(0);
const installState = ref<UpdateInstallSnapshot | null>(null);
const installBridgeAvailable = ref(false);
const installActionError = ref<InstallActionError>(null);

const WAITING_REASONS: ReadonlySet<string> = new Set([
  "active_work",
  "request_in_flight",
  "executor_starting",
]);
const BLOCKED_REASONS: ReadonlySet<string> = new Set([
  "runtime_unavailable",
  "runtime_changed",
  "executor_unconfirmed",
  "prepare_failed",
  "stop_unconfirmed",
  "restart_required",
  "update_unavailable",
  "install_failed",
]);
const INSTALL_PHASES: ReadonlySet<string> = new Set([
  "idle",
  "waiting",
  "preparing",
  "stopping",
  "installing",
  "blocked",
]);

/**
 * 精确校验宿主快照：未知字段/非法组合一律拒绝，旧 revision 不得覆盖新状态。
 * 这是 UI 侧的 fail-closed 门禁，不替代 Main 的授权与 schema 校验。
 */
export function isUpdateInstallSnapshot(value: unknown): value is UpdateInstallSnapshot {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return false;
  const body = value as Record<string, unknown>;
  const keys = Object.keys(body);
  if (keys.length !== 6) return false;
  for (const key of ["schemaVersion", "revision", "phase", "reason", "version", "canCancel"]) {
    if (!(key in body)) return false;
  }
  if (body.schemaVersion !== 1) return false;
  if (
    typeof body.revision !== "number" ||
    !Number.isSafeInteger(body.revision) ||
    body.revision < 1
  ) {
    return false;
  }
  if (typeof body.phase !== "string" || !INSTALL_PHASES.has(body.phase)) return false;
  if (typeof body.canCancel !== "boolean") return false;
  if (
    body.version !== null &&
    (typeof body.version !== "string" || body.version.length === 0 || body.version.length > 200)
  ) {
    return false;
  }
  const reason = body.reason;
  if (body.phase === "waiting") {
    if (body.canCancel !== true) return false;
    if (typeof body.version !== "string") return false;
    if (reason !== null && (typeof reason !== "string" || !WAITING_REASONS.has(reason))) return false;
    return true;
  }
  if (body.canCancel !== false) return false;
  if (body.phase === "idle") return body.version === null && reason === null;
  if (body.phase === "preparing" || body.phase === "stopping" || body.phase === "installing") {
    return typeof body.version === "string" && reason === null;
  }
  return typeof reason === "string" && BLOCKED_REASONS.has(reason);
}

/** 只接受严格递增的宿主 revision；旧/畸形快照不能推进安装。 */
function applyInstallSnapshot(payload: unknown): boolean {
  if (!isUpdateInstallSnapshot(payload)) return false;
  const current = installState.value;
  if (current && payload.revision <= current.revision) return false;
  installState.value = { ...payload };
  return true;
}

function applyInstallResult(result: unknown): void {
  if (result === null || typeof result !== "object") {
    installActionError.value = "failed";
    return;
  }
  const record = result as Record<string, unknown>;
  if (record.ok === true) {
    if (!applyInstallSnapshot(record.data)) {
      // 成功信封里的畸形/过期快照不能当成已推进状态。
      if (!isUpdateInstallSnapshot(record.data)) installActionError.value = "failed";
      return;
    }
    installActionError.value = null;
    return;
  }
  const error = record.error;
  const code =
    error !== null && typeof error === "object"
      ? (error as Record<string, unknown>).code
      : undefined;
  installActionError.value =
    code === "UPDATE_INSTALL_ACCESS_DENIED"
      ? "denied"
      : code === "UPDATE_INSTALL_UNAVAILABLE"
        ? "unavailable"
        : "failed";
}

const installBusy = computed(() => {
  const current = installState.value?.phase;
  return current === "preparing" || current === "stopping" || current === "installing";
});
const canCancelInstall = computed(() => installState.value?.canCancel === true);
const installWaiting = computed(() => installState.value?.phase === "waiting");

let subscribed = false;
// 同一版本只弹一次系统通知；持久化以覆盖 Renderer 刷新后的重放。
const UPDATE_NOTIFIED_KEY = "yaomu.update.notifiedVersion";

function loadNotifiedVersion(): string | null {
  try {
    const value = localStorage.getItem(UPDATE_NOTIFIED_KEY);
    return value ? value : null;
  } catch {
    return null;
  }
}

function rememberNotifiedVersion(version: string | null): void {
  notifiedVersion = version;
  try {
    if (version) localStorage.setItem(UPDATE_NOTIFIED_KEY, version);
    else localStorage.removeItem(UPDATE_NOTIFIED_KEY);
  } catch {
    // 存储不可用时只影响跨刷新去重，不阻塞更新状态。
  }
}

let notifiedVersion: string | null = loadNotifiedVersion();

function subscribeOnce(): void {
  if (subscribed) return;
  const update = window.yaomu?.update;
  if (!update) return;
  subscribed = true;
  const settings = useSettingsStore();

  update.onStatus?.((status) => {
    if (status === "checking") {
      // 周期/聚焦检查不得覆盖已有更新状态；仅在空闲或错误后进入 checking。
      if (phase.value === "idle" || phase.value === "error") {
        phase.value = "checking";
      }
    } else if (status === "idle") {
      // 后台检查失败静默回落；手动错误仍走 onError。
      if (phase.value === "checking") phase.value = "idle";
    } else if (status === "available") {
      // 自动下载失败或后台检查失败时回到可重试的 available，不重复弹通知。
      // 旧桥没有 ready 重放时 version 可能为空，等真正的 available 事件补齐。
      if (
        version.value.length > 0 &&
        (phase.value === "idle" ||
          phase.value === "checking" ||
          phase.value === "downloading")
      ) {
        phase.value = "available";
      }
    }
  });
  update.onAvailable?.((info) => {
    const sameVersion = version.value === info.version;
    const keepProgress =
      sameVersion && (phase.value === "downloading" || phase.value === "downloaded");
    version.value = info.version;
    releaseNotes.value = info.releaseNotes ?? "";
    // 同一版本已下载/下载中时，周期检查不得把状态降回 available。
    if (!keepProgress) phase.value = "available";
    // 同一版本只弹一次系统通知；站内通知中心仍可随重放恢复。
    const firstNotification = notifiedVersion !== info.version;
    if (firstNotification) rememberNotifiedVersion(info.version);
    useNotificationsStore().push({
      id: `update:available:${info.version}`,
      kind: "update",
      tone: "info",
      title: t("notify.update.title"),
      body: `v${info.version}`,
      route: "/settings/about",
    });
    // 分类开关在 UI（settings store），主进程只按请求发系统通知。
    if (firstNotification && settings.notifications.updateAvailable) {
      const notify = window.yaomu?.api?.notify;
      if (notify) void notify(t("notify.update.title"), `v${info.version}`, "update").catch(() => undefined);
    }
  });
  update.onNotAvailable?.(() => {
    rememberNotifiedVersion(null);
    // 已下载/下载中不因并发检查被清掉；其余状态回到 idle。
    if (
      phase.value === "checking" ||
      phase.value === "available" ||
      phase.value === "error"
    ) {
      phase.value = "idle";
    }
  });
  update.onProgress?.((p) => {
    progress.value = Math.round(p.percent);
    phase.value = "downloading";
  });
  update.onDownloaded?.((info) => {
    version.value = info.version;
    progress.value = 100;
    phase.value = "downloaded";
  });
  // 原始错误文本不进 UI：只显示已注册的固定文案，细节留在主进程日志。
  update.onError?.(() => {
    // 已下载版本不因一次检查错误丢失顶栏安装入口；安装错误仍由 install state 展示。
    if (phase.value !== "downloaded") phase.value = "error";
  });

  // 事件监听就绪后请求一次当前状态；AppShell/About 可能晚于启动检查挂载。
  if (typeof update.ready === "function") {
    void Promise.resolve(update.ready()).catch(() => undefined);
  }

  // 新 preload 才提供受守卫的安装状态桥；旧桥缺少这些方法时绝不回退到旧 install。
  const supportsGuardedInstall =
    typeof update.getInstallState === "function" &&
    typeof update.cancelInstall === "function" &&
    typeof update.onInstallState === "function" &&
    typeof update.install === "function";
  installBridgeAvailable.value = supportsGuardedInstall;
  if (!supportsGuardedInstall) return;
  // 先登记推送，再读取当前快照；旧 revision 由 applyInstallSnapshot 丢弃。
  update.onInstallState((payload) => {
    applyInstallSnapshot(payload);
  });
  void Promise.resolve(update.getInstallState())
    .then((result) => applyInstallResult(result))
    .catch(() => {
      installActionError.value = "unavailable";
    });
}

export function useUpdater() {
  subscribeOnce();

  const update = window.yaomu?.update;
  return {
    phase,
    version,
    releaseNotes,
    progress,
    installState,
    installBridgeAvailable,
    installActionError,
    installBusy,
    canCancelInstall,
    installWaiting,
    check() {
      // 安装准备/停止期间不允许重复检查或下载，避免并发改变待装版本。
      if (!update || typeof update.check !== "function" || installBusy.value) return;
      // 已下载/下载中时保留原状态；手动检查发现新版本会由 available 事件推进。
      if (phase.value !== "downloaded" && phase.value !== "downloading") {
        phase.value = "checking";
      }
      void update.check().catch(() => {
        phase.value = "error";
      });
    },
    download() {
      if (!update || typeof update.download !== "function" || installBusy.value) return;
      phase.value = "downloading";
      void update.download().catch(() => {
        phase.value = "error";
      });
    },
    /**
     * 开始或加入当前显式安装同意。只有新桥可用时才调用；
     * 旧 preload 的 install 是未经守卫的直装入口，绝不回退使用。
     */
    async install(): Promise<void> {
      installActionError.value = null;
      if (!installBridgeAvailable.value || !update || typeof update.install !== "function") {
        installActionError.value = "unavailable";
        return;
      }
      try {
        applyInstallResult(await update.install());
      } catch {
        installActionError.value = "failed";
      }
    },
    /** 仅 waiting 阶段由 Main 接受；准备/停止/安装中调用只返回当前快照。 */
    async cancelInstall(): Promise<void> {
      if (!installBridgeAvailable.value || !update || typeof update.cancelInstall !== "function") return;
      try {
        applyInstallResult(await update.cancelInstall());
      } catch {
        installActionError.value = "failed";
      }
    },
  };
}
