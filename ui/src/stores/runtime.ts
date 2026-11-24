// 宿主运行时就绪 store（契约 contracts/desktop-runtime.md / schema v1）。
//
// Main 是 phase/revision/generation 的唯一权威；Renderer 只消费受控快照：
// - 先订阅 onChanged，再 getSnapshot，按 revision 只接受更高版本（同版本幂等）；
// - 低版本、非法 shape、generation 回退一律忽略，旧回包不能覆盖新状态；
// - 快照只含有限 phase/reason，不保存端口、令牌、路径或原始异常。
//
// 旧完整桥没有 runtime 接口、显式 demo 与 unavailable 模式保持原有加载路径：
// 这些模式下 firstReady 直接为 true，不等待宿主桥、不落 mock。
import { defineStore } from "pinia";
import { computed, ref } from "vue";
import { safeYaomu } from "@/api/bridge-access";

const PHASES: readonly YaomuRuntimePhase[] = [
  "preparing",
  "starting",
  "ready",
  "restarting",
  "failed",
  "stopping",
  "stopped",
];

const REASONS: readonly YaomuRuntimeReason[] = [
  "startup_failed",
  "core_exited",
  "cleanup_unconfirmed",
  "handshake_failed",
  "protocol_mismatch",
  "workspace_mismatch",
];

/** 未确认清理/协议或 workspace 不匹配在本会话内不可重试。 */
const NON_RETRYABLE_REASONS: readonly YaomuRuntimeReason[] = [
  "cleanup_unconfirmed",
  "protocol_mismatch",
  "workspace_mismatch",
];

const SNAPSHOT_KEYS = new Set([
  "schemaVersion",
  "revision",
  "generation",
  "phase",
  "reason",
  "canRetry",
]);

function isSafeInt(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value);
}

function isPhase(value: unknown): value is YaomuRuntimePhase {
  return typeof value === "string" && (PHASES as readonly string[]).includes(value);
}

function isReason(value: unknown): value is YaomuRuntimeReason {
  return typeof value === "string" && (REASONS as readonly string[]).includes(value);
}

/** 严格按 schema v1 解析：拒绝额外字段、未知值和不满足 allOf 约束的快照。 */
export function parseRuntimeSnapshot(value: unknown): YaomuRuntimeSnapshot | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const raw = value as Record<string, unknown>;
  if (Object.keys(raw).length !== SNAPSHOT_KEYS.size) return null;
  for (const key of Object.keys(raw)) {
    if (!SNAPSHOT_KEYS.has(key)) return null;
  }
  if (raw.schemaVersion !== 1) return null;
  if (!isSafeInt(raw.revision) || raw.revision < 1) return null;
  if (!isSafeInt(raw.generation) || raw.generation < 0) return null;
  if (!isPhase(raw.phase)) return null;
  if (typeof raw.canRetry !== "boolean") return null;

  const phase = raw.phase;
  const reason = raw.reason;
  const canRetry = raw.canRetry;
  if (phase === "failed") {
    if (!isReason(reason)) return null;
  } else {
    if (reason !== null || canRetry !== false) return null;
  }
  if (phase === "ready" && raw.generation < 1) return null;
  if (isReason(reason) && NON_RETRYABLE_REASONS.includes(reason) && canRetry !== false) {
    return null;
  }

  return {
    schemaVersion: 1,
    revision: raw.revision,
    generation: raw.generation,
    phase,
    reason: phase === "failed" ? (reason as YaomuRuntimeReason) : null,
    canRetry,
  };
}

/** 只接受完整 runtime 三方法；缺方法视为旧桥/演示路径，不伪造状态。 */
function detectBridge(): YaomuRuntimeBridge | null {
  if (typeof window === "undefined") return null;
  const runtime = safeYaomu()?.runtime;
  if (!runtime) return null;
  if (
    typeof runtime.getSnapshot !== "function" ||
    typeof runtime.onChanged !== "function" ||
    typeof runtime.retry !== "function"
  ) {
    return null;
  }
  return runtime;
}

function isReadyState(phase: YaomuRuntimePhase, generation: number): boolean {
  return phase === "ready" && generation >= 1;
}

export const useRuntimeStore = defineStore("runtime", () => {
  const bridge = detectBridge();
  /** runtime：使用宿主就绪桥；legacy：旧桥/演示/无桥，沿用现有加载路径。 */
  const mode = ref<"runtime" | "legacy">(bridge ? "runtime" : "legacy");

  const phase = ref<YaomuRuntimePhase>("preparing");
  const reason = ref<YaomuRuntimeReason | null>(null);
  const revision = ref(0);
  const generation = ref(0);
  const canRetry = ref(false);
  /** 首次 ready 后为 true；后续失联不再卸载 AppShell/RouterView。 */
  const firstReady = ref(mode.value === "legacy");
  /** 每次从非 ready 进入 ready 递增；用于按“新连接”刷新只读缓存。 */
  const readyEpoch = ref(0);
  const retrying = ref(false);
  /** 受控拒绝信封里的安全错误码（不保存原始异常）。 */
  const accessError = ref<"" | "LOCAL_ACCESS_DENIED" | "CORE_NOT_READY">("");

  const gated = computed(() => mode.value === "runtime");
  const ready = computed(() => mode.value === "legacy" || isReadyState(phase.value, generation.value));
  /** 已就绪连接的 generation；失联时为 0，仅供只读诊断。 */
  const readyGeneration = computed(() => (ready.value ? generation.value : 0));
  const accessErrorKey = computed(() => {
    if (accessError.value === "LOCAL_ACCESS_DENIED") return "api.accessDenied";
    if (accessError.value === "CORE_NOT_READY") return "api.coreNotReady";
    return "";
  });

  let initialized = false;
  let unsubscribe: (() => void) | null = null;
  let unloadRegistered = false;

  function applySnapshot(value: unknown): boolean {
    const snapshot = parseRuntimeSnapshot(value);
    if (!snapshot) return false;
    // revision 单调；同版本幂等，低版本（含乱序回包）忽略。
    if (snapshot.revision <= revision.value) return false;
    // generation 不得倒退；异常快照按非法处理，不污染已确认连接。
    if (snapshot.generation < generation.value) return false;

    const wasReady = isReadyState(phase.value, generation.value);
    revision.value = snapshot.revision;
    phase.value = snapshot.phase;
    reason.value = snapshot.reason;
    canRetry.value = snapshot.canRetry;
    generation.value = snapshot.generation;
    accessError.value = "";

    const nowReady = isReadyState(snapshot.phase, snapshot.generation);
    if (nowReady) {
      firstReady.value = true;
      if (!wasReady) readyEpoch.value += 1;
    }
    return true;
  }

  function dispose(): void {
    unsubscribe?.();
    unsubscribe = null;
    initialized = false;
    if (unloadRegistered && typeof window !== "undefined") {
      window.removeEventListener("beforeunload", dispose);
      unloadRegistered = false;
    }
  }

  /** 先登记 changed 监听，再读取初始快照；旧 get 回包不会覆盖更新通知。 */
  async function init(): Promise<void> {
    if (!bridge || initialized) return;
    initialized = true;
    if (typeof window !== "undefined" && !unloadRegistered) {
      window.addEventListener("beforeunload", dispose);
      unloadRegistered = true;
    }
    try {
      unsubscribe = bridge.onChanged((snapshot) => {
        applySnapshot(snapshot);
      });
    } catch {
      accessError.value = "CORE_NOT_READY";
      return;
    }
    try {
      const result = await bridge.getSnapshot();
      if (!result.ok) {
        accessError.value = result.error.code;
        return;
      }
      applySnapshot(result.data);
    } catch {
      accessError.value = "CORE_NOT_READY";
    }
  }

  /**
   * 只重试宿主初始化，不恢复业务写请求。
   * 仅 failed && canRetry 时有效；并发调用合流，由 Main 的单一 Core 拥有者处理。
   */
  async function retry(): Promise<void> {
    if (!bridge || retrying.value) return;
    if (phase.value !== "failed" || !canRetry.value) return;
    retrying.value = true;
    accessError.value = "";
    try {
      const result = await bridge.retry();
      if (!result.ok) {
        accessError.value = result.error.code;
        return;
      }
      applySnapshot(result.data);
    } catch {
      accessError.value = "CORE_NOT_READY";
    } finally {
      retrying.value = false;
    }
  }

  return {
    mode,
    phase,
    reason,
    revision,
    generation,
    canRetry,
    firstReady,
    readyEpoch,
    retrying,
    accessError,
    accessErrorKey,
    gated,
    ready,
    readyGeneration,
    init,
    retry,
    dispose,
  };
});
