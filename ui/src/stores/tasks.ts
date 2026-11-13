// 任务 store：任务队列列表，订阅 Core 事件增量刷新。
// 快照高水位握手：先 GET /snapshot 取 H，再从 after_seq=H 订阅，避免回放与实时监听之间丢事件；
// 游标过期（EVENT_CURSOR_EXPIRED）时重新取快照，不静默宣称追赶完成。
//
// task.progress（seq=0）只做有界视图合并：严格校验 Task/project/step/attempt/epoch 身份，
// 50ms 合批渲染，不随进度发 HTTP、不推进持久游标、不改状态/排序。关键 task 事件立即清
// pending 并设置读栅栏，旧快照读不得回退新证据；同 attempt 旧读不得回退已收进度。
// 连接通知只触发一次合并只读刷新，不重新订阅（避免连接→读→重订阅循环）。
import { defineStore } from "pinia";
import { ref } from "vue";
import { t } from "@/i18n";
import { api } from "@/api";
import type { EventSubscriptionOptions } from "@/api/types";
import { createReadVersion, currentReadVersion } from "@/composables/readGeneration";
import {
  isSafePositiveInteger,
  parseTaskProgress,
  runningStepIdentity,
  type TaskProgressFrame,
} from "@/domain/taskProgress";
import type { DomainEvent, ID, Task, TaskStatus } from "@/domain/types";
import { useInstallationsStore } from "./installations";
import { useExecutionLogsStore } from "./executionLogs";
import { useNotificationsStore } from "./notifications";
import { useSettingsStore } from "./settings";

const TERMINAL_TASK_STATUSES: readonly TaskStatus[] = [
  "SUCCEEDED",
  "FAILED",
  "CANCELED",
  "INTERRUPTED",
];

/** 实时进度视图合并上限：同一窗口内每任务只渲染最后一个合法值。 */
const PROGRESS_FLUSH_MS = 50;

/** 仅视图元数据，不写入公开 Task DTO（Core 快照不公开 state_version）。 */
interface TaskViewState {
  /** 最近一次关键 task 事件的本地证据序号；读发起早于它时整条不得回退。 */
  criticalEvidence: number;
  /** 关键事件携带的 state_version；进度帧版本更低时拒绝。 */
  criticalVersion: number;
  /** 等待“关键事件之后发起”的权威快照/详情读回；期间拒收实时进度。 */
  fence: boolean;
  /** 最近一次已应用该任务读的跨域读取序号（list/snapshot/detail 共用）。 */
  lastReadSeq: number;
  /** 当前已接受的 RUNNING step attempt/epoch 与最新实时进度。 */
  attempt?: number;
  epoch?: number;
  progress?: number;
}

function maxDefined(...values: (number | undefined)[]): number | undefined {
  let result: number | undefined;
  for (const value of values) {
    if (value === undefined) continue;
    result = result === undefined ? value : Math.max(result, value);
  }
  return result;
}

export const useTasksStore = defineStore("tasks", () => {
  const items = ref<Task[]>([]);
  const loading = ref(false);
  /** 事件流停止的安全错误码（如 EVENT_CURSOR_EXPIRED）；空串表示同步正常。 */
  const syncError = ref("");
  /** 最近一次快照的事件高水位 H（诊断用，不作为业务状态）。 */
  const highWatermark = ref(0);
  let unsub: (() => void) | null = null;
  let reloadTimer: ReturnType<typeof setTimeout> | null = null;
  /** 最近一次绑定快照的项目；事件游标过期时用它重新获取快照。 */
  let currentProjectId: ID | null = null;
  let syncing = false;
  let pendingResync = false;
  let lastResyncAt = 0;
  // 订阅代次：stop/resync 递增后，旧快照请求与旧订阅回调即使晚回也不得生效。
  let listenEpoch = 0;
  let subscriptionSeq = 0;
  let restartPending = false;
  const listReads = createReadVersion();
  const snapshotReads = createReadVersion();

  // ---- 进度视图合并（有界、仅已载任务） ----
  const pendingProgress = new Map<ID, TaskProgressFrame>();
  const viewStates = new Map<ID, TaskViewState>();
  let progressTimer: ReturnType<typeof setTimeout> | null = null;
  /** 关键 task 事件的本地证据序号；用于拒绝发起更早的迟到读。 */
  let evidenceSeq = 0;
  /**
   * 跨读取域（list/snapshot/detail）的读取序号：每任务只接受“发起不早于
   * 已应用读”的结果。listReads/snapshotReads 只约束各自域，不能阻止旧 detail
   * 在较新 list 终态之后晚回并重新置 RUNNING。
   */
  let readSeq = 0;
  /**
   * loading 所有权序号：只由最新发起且未被 stop 作废的读清理加载态。
   * 不复用 read-validity（含 runtime generation），否则 generation 变化而
   * 无新读接手时 loading 会永久悬挂。
   */
  let loadingOwner = 0;

  // ---- 连接通知合并只读刷新 ----
  let connectionTimer: ReturnType<typeof setTimeout> | null = null;
  let connectionPredicate: (() => boolean) | null = null;
  let connectionRefreshing = false;
  let connectionPending = false;

  // ---- RUNNING task 详情水合（列表/项目快照不嵌入 steps） ----
  let hydrationEpoch = 0;
  const hydrationTokens = new Map<ID, object>();

  function ensureViewState(taskId: ID): TaskViewState {
    let view = viewStates.get(taskId);
    if (!view) {
      view = { criticalEvidence: 0, criticalVersion: 0, fence: false, lastReadSeq: 0 };
      viewStates.set(taskId, view);
    }
    return view;
  }

  function nextReadSeq(): number {
    readSeq += 1;
    return readSeq;
  }

  /** 只保留已载入任务的视图缓存，避免为离线任务维护第二队列。 */
  function pruneViewStates(): void {
    const ids = new Set(items.value.map((task) => task.id));
    for (const taskId of viewStates.keys()) {
      if (!ids.has(taskId)) viewStates.delete(taskId);
    }
    for (const taskId of pendingProgress.keys()) {
      if (!ids.has(taskId)) pendingProgress.delete(taskId);
    }
  }

  function clearPendingProgress(): void {
    if (progressTimer) {
      clearTimeout(progressTimer);
      progressTimer = null;
    }
    pendingProgress.clear();
  }

  /** stop/resync 必须取消已排队的关键事件重载，避免停止后仍 load/hydrate。 */
  function clearReloadTimer(): void {
    if (reloadTimer) {
      clearTimeout(reloadTimer);
      reloadTimer = null;
    }
  }

  function clearConnectionRefresh(): void {
    if (connectionTimer) {
      clearTimeout(connectionTimer);
      connectionTimer = null;
    }
    connectionPredicate = null;
    connectionPending = false;
  }

  function invalidateHydration(): void {
    hydrationEpoch += 1;
    hydrationTokens.clear();
  }

  /** 由一次权威读回（列表/项目快照/任务详情）合并单条任务。 */
  function reconcileTask(
    incoming: Task,
    evidence: number,
    current: Task | undefined,
    read: number,
  ): Task | null {
    const view = ensureViewState(incoming.id);
    // 同一任务跨 list/snapshot/detail：发起更早的读即使先被水合/后返回也不得覆盖。
    if (view.lastReadSeq > read) return null;
    // 即使本条因证据/身份被拒，也记录“该任务已被此读观察”，防止更早读随后回写。
    view.lastReadSeq = Math.max(view.lastReadSeq, read);
    // 读发起早于更新的关键事件：不得覆盖或回退新证据。
    if (view.criticalEvidence > evidence) return null;
    // 该读发起于关键事件之后：可以确认关键证据并解除栅栏。
    if (view.fence && evidence >= view.criticalEvidence) view.fence = false;

    let progress = incoming.progress;
    if (incoming.status === "RUNNING") {
      const identity = runningStepIdentity(incoming);
      if (identity) {
        if (view.attempt !== undefined && view.epoch !== undefined) {
          if (
            identity.attempt < view.attempt ||
            (identity.attempt === view.attempt && identity.epoch < view.epoch)
          ) {
            // 旧 attempt 快照晚回：保留当前/实时视图，不回退。
            return null;
          }
          if (identity.attempt === view.attempt && identity.epoch === view.epoch) {
            // 同一尝试：旧快照不得把已收进度改小。
            progress = maxDefined(progress, view.progress, current?.progress);
            view.progress = progress;
          } else {
            // 新尝试：正常替换进度。
            view.attempt = identity.attempt;
            view.epoch = identity.epoch;
            view.progress = progress;
          }
        } else {
          view.attempt = identity.attempt;
          view.epoch = identity.epoch;
          view.progress = progress;
        }
      } else {
        // 列表/项目快照不嵌入步骤：身份未知时保留已接受值，等详情读回。
        progress = maxDefined(progress, current?.progress, view.progress);
      }
    } else {
      // 终态/暂停等状态变化：状态权威替换，清空旧实时身份。
      view.attempt = undefined;
      view.epoch = undefined;
      view.progress = undefined;
    }
    return { ...incoming, progress };
  }

  /**
   * 合并一次权威读。projectId 存在时表示项目快照/分页，保留其他项目任务；
   * 否则为全局任务列表，按读回结果整体替换（仍保留被更新关键事件保护的条目）。
   */
  function applyTaskRead(
    pageItems: Task[],
    evidence: number,
    read: number,
    projectId?: ID,
  ): void {
    if (projectId) {
      const byId = new Map(items.value.map((task) => [task.id, task]));
      for (const incoming of pageItems) {
        const reconciled = reconcileTask(incoming, evidence, byId.get(incoming.id), read);
        if (reconciled) byId.set(incoming.id, reconciled);
      }
      items.value = [...byId.values()];
    } else {
      const currentById = new Map(items.value.map((task) => [task.id, task]));
      const included = new Set<ID>();
      const next: Task[] = [];
      for (const incoming of pageItems) {
        included.add(incoming.id);
        const current = currentById.get(incoming.id);
        const reconciled = reconcileTask(incoming, evidence, current, read);
        if (reconciled) next.push(reconciled);
        else if (current) next.push(current);
      }
      for (const [taskId, current] of currentById) {
        if (included.has(taskId)) continue;
        const view = viewStates.get(taskId);
        // 更早的全局 list 不得删除已被较新 snapshot/detail 读观察到的任务。
        if (view && (view.criticalEvidence > evidence || view.lastReadSeq > read)) {
          next.push(current);
        }
      }
      items.value = next;
    }
    pruneViewStates();
    hydrateRunningTasks();
  }

  /**
   * 列表/项目快照不嵌入 steps，无法校验进度身份；对 RUNNING 且步骤未知的任务
   * 读取一次详情（按任务去重，不随进度帧触发）。关键事件后的新读回会再次补。
   */
  function hydrateRunningTasks(): void {
    const epoch = hydrationEpoch;
    for (const task of items.value) {
      if (task.status !== "RUNNING") continue;
      if (task.steps && task.steps.length > 0) continue;
      if (hydrationTokens.has(task.id)) continue;
      if (viewStates.get(task.id)?.fence) continue;
      const token = {};
      hydrationTokens.set(task.id, token);
      const evidence = evidenceSeq;
      const read = nextReadSeq();
      const runtime = currentReadVersion();
      void (async () => {
        let superseded = false;
        try {
          const detail = await api.getTask(task.id);
          const nowVersion = currentReadVersion();
          if (
            epoch !== hydrationEpoch ||
            hydrationTokens.get(task.id) !== token ||
            runtime.generation !== nowVersion.generation ||
            runtime.epoch !== nowVersion.epoch
          ) {
            return;
          }
          const index = items.value.findIndex((item) => item.id === task.id);
          if (index < 0) return;
          const current = items.value[index];
          const reconciled = reconcileTask(detail, evidence, current, read);
          if (!reconciled) {
            // 被发起更晚的 list/snapshot 读取代、但仍 RUNNING 且无步骤身份：
            // 若不补一次水合，进度会永久被拒（旧 token 已占用本次机会）。
            const view = viewStates.get(task.id);
            superseded =
              view !== undefined &&
              view.lastReadSeq > read &&
              current.status === "RUNNING" &&
              (!current.steps || current.steps.length === 0);
            return;
          }
          const next = [...items.value];
          next[index] = reconciled;
          items.value = next;
        } catch {
          // 详情读取失败不伪造身份；下一次合并/关键事件再补（不在异常上递归）。
        } finally {
          if (hydrationTokens.get(task.id) === token) hydrationTokens.delete(task.id);
          // 仅“被较新读取代”补一次必要水合；失败/异常/旧身份淘汰不触发。
          if (superseded && epoch === hydrationEpoch) hydrateRunningTasks();
        }
      })();
    }
  }

  function scheduleProgressFlush(): void {
    if (progressTimer) return;
    progressTimer = setTimeout(() => {
      progressTimer = null;
      flushProgress();
    }, PROGRESS_FLUSH_MS);
  }

  /** 合批应用：重新校验排队期间可能变化的关键事件/状态/尝试身份。 */
  function flushProgress(): void {
    if (pendingProgress.size === 0) return;
    const frames = [...pendingProgress.values()];
    pendingProgress.clear();
    const indexById = new Map(items.value.map((task, index) => [task.id, index]));
    let next: Task[] | null = null;
    for (const frame of frames) {
      const index = indexById.get(frame.taskId);
      if (index === undefined) continue;
      const task = (next ?? items.value)[index];
      const view = ensureViewState(frame.taskId);
      if (view.fence) continue;
      if (view.criticalVersion > 0 && frame.stateVersion < view.criticalVersion) continue;
      if (task.status !== "RUNNING" || task.projectId !== frame.projectId) continue;
      const step = task.steps?.find((item) => item.stepId === frame.stepId);
      if (!step || step.status !== "RUNNING") continue;
      if (step.attemptNo !== frame.attempt || step.epoch !== frame.epoch) continue;
      if (
        view.attempt === frame.attempt &&
        view.epoch === frame.epoch &&
        view.progress !== undefined &&
        frame.progress < view.progress
      ) {
        continue;
      }
      view.attempt = frame.attempt;
      view.epoch = frame.epoch;
      view.progress = frame.progress;
      if (!next) next = [...items.value];
      next[index] = { ...task, progress: frame.progress };
    }
    if (next) items.value = next;
  }

  function handleProgress(event: DomainEvent): void {
    const frame = parseTaskProgress(event, items.value);
    if (!frame) return;
    const view = viewStates.get(frame.taskId);
    if (view?.fence) return;
    if (view && view.criticalVersion > 0 && frame.stateVersion < view.criticalVersion) return;
    // 有界：只保留每个已载任务的最新一帧。
    pendingProgress.set(frame.taskId, frame);
    scheduleProgressFlush();
  }

  /** 关键 task 事件：立即清 pending 并挡住迟到帧，直到事件之后的权威读回。 */
  function noteCriticalTaskEvent(event: DomainEvent): void {
    const taskId = event.taskId;
    if (!taskId) return;
    const task = items.value.find((item) => item.id === taskId);
    if (!task) return;
    evidenceSeq += 1;
    const view = ensureViewState(taskId);
    view.criticalEvidence = evidenceSeq;
    view.fence = true;
    if (isSafePositiveInteger(event.stateVersion)) {
      view.criticalVersion = Math.max(view.criticalVersion, event.stateVersion);
    }
    pendingProgress.delete(taskId);
  }

  async function load(projectId?: ID): Promise<void> {
    const token = listReads.begin();
    const owner = (loadingOwner += 1);
    const evidence = evidenceSeq;
    const read = nextReadSeq();
    loading.value = true;
    try {
      const page = await api.listTasks(projectId ? { projectId } : undefined);
      if (!listReads.current(token)) return;
      applyTaskRead(page.items, evidence, read, projectId);
    } finally {
      // loading 只按独立 owner 清理；读数据仍按 read-validity 丢弃。
      if (loadingOwner === owner) loading.value = false;
    }
  }

  function notifyTask(event: DomainEvent, tone: "done" | "failed"): void {
    const task = items.value.find((item) => item.id === event.taskId);
    // 快照历史事件可能补放；列表未就绪或任务已是终态时不重复通知。
    if (!task || TERMINAL_TASK_STATUSES.includes(task.status)) return;
    const titleKey = tone === "done" ? "notify.taskDone.title" : "notify.taskFailed.title";

    // 通知中心汇总：无论系统弹窗偏好如何都记录。
    useNotificationsStore().push({
      id: `${event.type}:${event.taskId}:${event.seq}`,
      kind: "task",
      tone: tone === "done" ? "success" : "error",
      title: t(titleKey),
      body: task.label || t("notify.taskFallback"),
      route: task.projectId ? `/projects/${task.projectId}` : "/projects",
    });

    // 系统级弹窗按用户偏好下发。
    const prefs = useSettingsStore().notifications;
    if (tone === "done" && !prefs.taskDone) return;
    if (tone === "failed" && !prefs.taskFailed) return;
    const notify = typeof window !== "undefined" ? window.yaomu?.api?.notify : undefined;
    if (!notify) return;
    void notify(t(titleKey), task.label || t("notify.taskFallback"), "task").catch(
      () => undefined,
    );
  }

  /** 合并短时间内的大量关键事件，避免初次回放或突发状态事件触发请求风暴。 */
  function scheduleReload(): void {
    if (reloadTimer) return;
    reloadTimer = setTimeout(() => {
      reloadTimer = null;
      void load().catch(() => undefined);
    }, 200);
  }

  function handleEvent(event: DomainEvent): void {
    syncError.value = "";
    if (event.type === "task.progress") {
      // 可丢弃实时进度：只做视图合并，不发 HTTP、不改状态/游标。
      handleProgress(event);
      return;
    }
    if (event.type === "task.execution_log") {
      // 过程行高频且可丢弃：只进日志缓冲，不触发任务列表重载。
      useExecutionLogsStore().handleEvent(event);
      return;
    }
    if (event.type.startsWith("installation.")) {
      useInstallationsStore().handleEvent(event);
      return;
    }
    if (event.type.startsWith("task.")) noteCriticalTaskEvent(event);
    if (event.type.startsWith("task.") || event.type.startsWith("run.")) scheduleReload();
    if (event.type === "task.succeeded") notifyTask(event, "done");
    else if (event.type === "task.failed") notifyTask(event, "failed");
  }

  /**
   * 连接通知合并刷新：同一时间至多一个只读读回，期间再来的通知最多补一次；
   * 刷新成功不重新订阅，避免连接→读→重订阅循环。
   */
  function scheduleConnectionRefresh(isCurrent: () => boolean): void {
    connectionPredicate = isCurrent;
    if (connectionTimer) return;
    connectionTimer = setTimeout(() => {
      connectionTimer = null;
      const predicate = connectionPredicate;
      connectionPredicate = null;
      if (!predicate || !predicate()) return;
      void refreshAfterConnect();
    }, 0);
  }

  async function refreshAfterConnect(): Promise<void> {
    if (connectionRefreshing) {
      connectionPending = true;
      return;
    }
    connectionRefreshing = true;
    try {
      // 已停止/已替换的订阅不得再触发读回。
      if (!unsub) return;
      const epoch = listenEpoch;
      const evidence = evidenceSeq;
      if (currentProjectId) {
        const token = snapshotReads.begin();
        // 项目快照优先于更早发起的全局 list：旧 list 不得在快照后整体替换 items。
        listReads.invalidate();
        const read = nextReadSeq();
        try {
          const snapshot = await api.getSnapshot(currentProjectId);
          if (epoch !== listenEpoch || !snapshotReads.current(token)) return;
          applyTaskRead(snapshot.tasks, evidence, read, currentProjectId);
        } catch {
          // 只读恢复失败保持现有视图；连接本身仍由事件流负责。
        }
        return;
      }
      const token = listReads.begin();
      const owner = (loadingOwner += 1);
      const read = nextReadSeq();
      try {
        const page = await api.listTasks();
        if (epoch !== listenEpoch || !listReads.current(token)) return;
        applyTaskRead(page.items, evidence, read);
      } catch {
        // 同上：不把恢复读失败伪装成业务错误。
      } finally {
        if (loadingOwner === owner) loading.value = false;
      }
    } finally {
      connectionRefreshing = false;
      if (connectionPending) {
        connectionPending = false;
        scheduleConnectionRefresh(() => unsub !== null);
      }
    }
  }

  function subscribe(afterSeq?: number): void {
    unsub?.();
    const epoch = listenEpoch;
    const subscription = (subscriptionSeq += 1);
    const isCurrent = () => epoch === listenEpoch && subscription === subscriptionSeq;
    const options: EventSubscriptionOptions = {
      onConnected: () => {
        if (!isCurrent()) return;
        scheduleConnectionRefresh(isCurrent);
      },
    };
    if (afterSeq !== undefined) options.afterSeq = afterSeq;
    unsub = api.subscribeEvents(
      (event) => {
        // 旧订阅/旧流的迟到回调不得写入当前视图。
        if (isCurrent()) handleEvent(event);
      },
      (error) => {
        if (!isCurrent()) return;
        if (error.code === "EVENT_CURSOR_EXPIRED") {
          // 历史已裁剪：只能重新获取快照，不能假装已补齐。
          void resync();
          return;
        }
        syncError.value = error.code || "CORE_NOT_READY";
      },
      options,
    );
  }

  /** 重新握手：快照 → H → 从 H 订阅；快照不可用时退回全量重载 + 从 0 订阅。 */
  async function resync(force = false): Promise<void> {
    if (syncing) {
      // 已有重同步在飞行：项目切换（force）排队等它结束后按新项目重绑。
      if (force) pendingResync = true;
      return;
    }
    // 防止游标持续失效时紧密循环；项目中主动切换（force）不受冷却限制。
    const startedAt = Date.now();
    if (!force && startedAt - lastResyncAt < 2_000) {
      syncError.value = "EVENT_CURSOR_EXPIRED";
      return;
    }
    lastResyncAt = startedAt;
    syncing = true;
    // 重建订阅：先作废旧回调/排队工作/在飞读，再按新代次握手。
    const epoch = (listenEpoch += 1);
    unsub?.();
    unsub = null;
    // 旧全局 list/详情读不得在项目快照之后晚回并整体替换新 items。
    listReads.invalidate();
    clearReloadTimer();
    clearPendingProgress();
    clearConnectionRefresh();
    invalidateHydration();
    const token = snapshotReads.begin();
    const evidence = evidenceSeq;
    try {
      syncError.value = "";
      const projectId = currentProjectId;
      if (projectId) {
        try {
          const read = nextReadSeq();
          const snapshot = await api.getSnapshot(projectId);
          // 停止/重连后旧快照不得重新绑定订阅。
          if (epoch !== listenEpoch || !snapshotReads.current(token)) return;
          highWatermark.value = snapshot.eventSeq;
          applyTaskRead(snapshot.tasks, evidence, read, projectId);
          subscribe(snapshot.eventSeq);
          return;
        } catch {
          // Core 不可用或项目已删除：退回全量重载，界面通过 load 的错误保持可读。
          if (epoch !== listenEpoch || !snapshotReads.current(token)) return;
        }
      }
      await load(projectId ?? undefined).catch(() => undefined);
      if (epoch !== listenEpoch || !snapshotReads.current(token) || unsub) return;
      subscribe();
    } finally {
      syncing = false;
      if (pendingResync) {
        pendingResync = false;
        void resync(true);
      }
      // stop→start 交错时，等待旧 resync 收敛后再补订阅。
      if (restartPending) {
        restartPending = false;
        void startListening();
      }
    }
  }

  async function startListening(projectId?: ID): Promise<void> {
    if (projectId) currentProjectId = projectId;
    if (unsub) return;
    if (syncing) {
      // 旧订阅正在收敛：结束后按当前项目补一次启动，避免 stop/start 交错丢订阅。
      restartPending = true;
      return;
    }
    const epoch = (listenEpoch += 1);
    // 新握手开始后，旧全局 list 不得晚回替换即将合并的项目快照。
    listReads.invalidate();
    clearPendingProgress();
    clearConnectionRefresh();
    invalidateHydration();
    const token = snapshotReads.begin();
    const evidence = evidenceSeq;
    if (currentProjectId) {
      syncing = true;
      try {
        const read = nextReadSeq();
        const snapshot = await api.getSnapshot(currentProjectId);
        if (epoch !== listenEpoch || !snapshotReads.current(token)) return;
        highWatermark.value = snapshot.eventSeq;
        applyTaskRead(snapshot.tasks, evidence, read, currentProjectId);
        subscribe(snapshot.eventSeq);
        return;
      } catch {
        // Core 不可用：仍订阅事件流，由错误通道反馈可读状态。
        if (epoch !== listenEpoch || !snapshotReads.current(token)) return;
      } finally {
        syncing = false;
        if (restartPending) {
          restartPending = false;
          void startListening();
        }
      }
    }
    if (epoch !== listenEpoch || !snapshotReads.current(token) || unsub) return;
    subscribe();
  }

  /** 绑定项目并重新握手（快照高水位 → 订阅）；项目切换/进入译制页时调用。 */
  async function syncProject(projectId: ID): Promise<void> {
    currentProjectId = projectId;
    await resync(true);
  }

  function stopListening(): void {
    // 先作废在飞快照/列表、订阅代次与排队进度，再取消订阅；晚回包不得重新绑定。
    listenEpoch += 1;
    subscriptionSeq += 1;
    snapshotReads.invalidate();
    listReads.invalidate();
    restartPending = false;
    pendingResync = false;
    clearReloadTimer();
    clearPendingProgress();
    clearConnectionRefresh();
    invalidateHydration();
    // 停止后旧实时视图不再可信；重连由快照恢复。
    viewStates.clear();
    // 作废所有在飞读的 loading 所有权；停止后不得由旧 finally 改写加载态。
    loadingOwner += 1;
    loading.value = false;
    unsub?.();
    unsub = null;
  }

  /** 宿主重连后重建订阅：先读一次最新列表，再按当前项目重新握手。 */
  async function reconnect(): Promise<void> {
    stopListening();
    await load().catch(() => undefined);
    await startListening();
  }

  async function retry(id: ID): Promise<void> {
    await api.retryTask(id);
    await load();
  }

  async function cancel(id: ID): Promise<void> {
    await api.cancelTask(id);
    await load();
  }

  return {
    items,
    loading,
    syncError,
    highWatermark,
    load,
    startListening,
    syncProject,
    stopListening,
    reconnect,
    retry,
    cancel,
  };
});
