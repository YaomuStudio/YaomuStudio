import { expect, type Page } from "@playwright/test";

// 浏览器上下文没有 window.yaomu 桥；playwright.config.ts 的 webServer 显式设置
// VITE_E2E_FIXTURES=1，api 门面才会加载测试夹具（产品无演示模式）。
// 预置已引导状态，避免每个用例都先走 onboarding；locale 固定英文以保证可选择器稳定。
export async function seedOnboardedSettings(page: Page): Promise<void> {
  await page.addInitScript(() => {
    localStorage.setItem("yaomu.settings", JSON.stringify({ onboarded: true, locale: "en" }));
  });
}

/**
 * 保留一个 DEV mock 控制对象，供用例读取 mock 侧记录（如 lastRunInput）。
 * 在首次导航前调用；与 seedMockFault 共用同一个对象，fault 仍可注入。
 */
export async function seedMockControl(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    const control = (scope.__yaomuMock ??= {});
    // 业务调用探针：宿主首次 ready 前应为空，只有页面真正读取后才出现方法名。
    if (!Array.isArray(control.businessCalls)) control.businessCalls = [];
  });
}

/**
 * 注入 mock 故障（仅 `import.meta.env.DEV` 下生效，见 ui/src/api/mock.ts）。
 * 必须在首次导航前调用；清除用 clearMockFault，用来验证错误态可恢复。
 */
export async function seedMockFault(page: Page, fault: string): Promise<void> {
  await page.addInitScript((value) => {
    (window as Window & { __yaomuMock?: { fail?: string } }).__yaomuMock = { fail: value };
  }, fault);
}

export async function clearMockFault(page: Page): Promise<void> {
  await page.evaluate(() => {
    const control = (window as Window & { __yaomuMock?: { fail?: string | string[] } })
      .__yaomuMock;
    if (control) delete control.fail;
  });
}

/**
 * 覆盖 mock 能力目录的槽位状态（仅 DEV 生效，见 ui/src/api/mock.ts）。
 * 必须在首次导航前调用；缺省状态为 READY。
 */
export async function seedCapabilityStates(
  page: Page,
  states: Partial<Record<string, string>>,
): Promise<void> {
  await page.addInitScript((value) => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    scope.__yaomuMock = { ...(scope.__yaomuMock ?? {}), capabilityStates: value };
  }, states);
}

/**
 * 覆盖 mock 能力槽的展示字段（引擎/版本/语言等，空值表示 Core 未提供）。
 * 仅 DEV 生效，见 ui/src/api/mock.ts；必须在首次导航前调用。
 */
export async function seedCapabilityFixtures(
  page: Page,
  fixtures: Record<string, Record<string, unknown>>,
): Promise<void> {
  await page.addInitScript((value) => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    scope.__yaomuMock = { ...(scope.__yaomuMock ?? {}), capabilityFixtures: value };
  }, fixtures);
}

/**
 * 替换指定项目的资产列表（仅 DEV 生效，见 ui/src/api/mock.ts）。
 * 用于覆盖卡片下载/预览的角色、语言、文件名与混合旧资产组合。
 */
export async function seedAssetFixtures(
  page: Page,
  fixtures: Record<string, unknown[]>,
): Promise<void> {
  await page.addInitScript((value) => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    scope.__yaomuMock = { ...(scope.__yaomuMock ?? {}), assetFixtures: value };
  }, fixtures);
}

// ---------------------------------------------------------------------------
// 共享发布安装 fixture（仅 DEV mock）
//
// 覆盖范围/复用目标/安装记录都可以显式覆盖，以便构造共享安装、旧 Core 单槽
// 形状与异常范围；mock 只模拟控制面事实，不冒充真实下载或 Core 事务。
// ---------------------------------------------------------------------------

export interface InstallationControlOptions {
  /** 按请求槽覆盖计划覆盖范围；null=省略字段（旧 Core 单槽），数组=显式范围。 */
  planCoverage?: Record<string, string[] | null>;
  /** 按请求槽强制计划复用目标 ID（可为不存在的 ID，验证 stale 失败）。 */
  planReuse?: Record<string, string>;
  /** 预置安装记录；旧 Core 形状可省略 coveredSlots/boundSlots/currentSlots。 */
  installationFixtures?: Record<string, unknown>[];
  /** 与安装记录配套的初始能力状态（如两个槽位都在 NEEDS_SETUP）。 */
  capabilityStates?: Record<string, string>;
}

export async function seedInstallationControl(
  page: Page,
  options: InstallationControlOptions,
): Promise<void> {
  await page.addInitScript((value) => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    const control = (scope.__yaomuMock ??= {});
    if (value.planCoverage) control.planCoverage = value.planCoverage;
    if (value.planReuse) control.planReuse = value.planReuse;
    if (value.installationFixtures) control.installationFixtures = value.installationFixtures;
    if (value.capabilityStates) control.capabilityStates = value.capabilityStates;
  }, options);
}

export interface InstallationProbe {
  starts: number;
  installRequests: number;
  planRequests: number;
  cancelIds: string[];
}

/** 读取 mock 侧物理安装启动/install 请求次数、计划请求次数与取消命中的安装 ID。 */
export async function readInstallationProbe(page: Page): Promise<InstallationProbe> {
  return page.evaluate(() => {
    const control = (
      window as unknown as {
        __yaomuMock?: {
          installationStarts?: number;
          installRequests?: number;
          planRequests?: number;
          cancelInstallationIds?: string[];
        };
      }
    ).__yaomuMock;
    return {
      starts: Number(control?.installationStarts) || 0,
      installRequests: Number(control?.installRequests) || 0,
      planRequests: Number(control?.planRequests) || 0,
      cancelIds: Array.isArray(control?.cancelInstallationIds)
        ? [...control.cancelInstallationIds]
        : [],
    };
  });
}

/** 读取 mock 侧当前安装记录（验证一份实体/引用投影，不影响 UI 缓存）。 */
export async function readMockInstallations(page: Page): Promise<Record<string, unknown>[]> {
  return page.evaluate(() => {
    const control = (
      window as unknown as {
        __yaomuMock?: { readInstallations?: () => unknown };
      }
    ).__yaomuMock;
    const value = control?.readInstallations?.();
    return Array.isArray(value) ? (value as Record<string, unknown>[]) : [];
  });
}

/** 让指定物理安装完成（模拟 Core 全部覆盖槽同一事务激活）。 */
export async function completeMockInstallation(page: Page, installationId: string): Promise<void> {
  await page.evaluate((id) => {
    const control = (
      window as unknown as {
        __yaomuMock?: { completeInstallation?: (installationId: string) => void };
      }
    ).__yaomuMock;
    control?.completeInstallation?.(id);
  }, installationId);
}

/** 让指定物理安装失败（不激活新版本）。 */
export async function failMockInstallation(page: Page, installationId: string): Promise<void> {
  await page.evaluate((id) => {
    const control = (
      window as unknown as {
        __yaomuMock?: { failInstallation?: (installationId: string) => void };
      }
    ).__yaomuMock;
    control?.failInstallation?.(id);
  }, installationId);
}

/** 推送指定共享安装的进度。 */
export async function setMockInstallationProgress(
  page: Page,
  installationId: string,
  progress: number,
): Promise<void> {
  await page.evaluate(
    ({ id, value }) => {
      const control = (
        window as unknown as {
          __yaomuMock?: {
            setInstallationProgress?: (installationId: string, progress: number) => void;
          };
        }
      ).__yaomuMock;
      control?.setInstallationProgress?.(id, value);
    },
    { id: installationId, value: progress },
  );
}

/**
 * 覆盖指定项目 Run.params 的冻结字段（仅 DEV mock）。
 * 值为 null 表示删除该字段，用于验证“未记录”不被猜成 false。
 */
export async function seedRunParams(
  page: Page,
  fixtures: Record<string, Record<string, boolean | null>>,
): Promise<void> {
  await page.addInitScript((value) => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    scope.__yaomuMock = { ...(scope.__yaomuMock ?? {}), runParamsFixtures: value };
  }, fixtures);
}

/**
 * 预置旧 custom 模板（仅 DEV mock），用于恢复 adjustDuration=true 的历史模板。
 * 模板不会自动写回；只有显式保存才会持久化。
 */
export async function seedCustomTemplates(page: Page, templates: unknown[]): Promise<void> {
  await page.addInitScript((value) => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    scope.__yaomuMock = { ...(scope.__yaomuMock ?? {}), customTemplateFixtures: value };
  }, templates);
}

/** 在 Projects 页新建项目并进入详情，返回 mock 分配的 project id。 */
export async function createProject(page: Page, name: string): Promise<string> {
  await page.goto("/#/projects");
  await page.getByTestId("project-create").click();
  await page.getByTestId("project-create-input").fill(name);
  await page.getByTestId("project-create-confirm").click();
  await expect(page).toHaveURL(/#\/projects\/proj-/);

  const match = /#\/projects\/(proj-[^/?]+)/.exec(page.url());
  if (!match) throw new Error(`Unexpected project URL: ${page.url()}`);
  return match[1];
}

/** 用应用内导航进入全流程页，保留同一页面会话中的 mock 数据。 */
export async function openPipelineForProject(page: Page, projectId: string): Promise<void> {
  await page.goto(`/#/projects/${projectId}/pipeline`);
  await expect(page).toHaveURL(new RegExp(`#/projects/${projectId}/pipeline$`));
  await expect(page.getByRole("heading", { level: 1, name: "Full Pipeline" })).toBeVisible();
}

/** 小型合成文件：不引入真实媒体，只验证 UI 导入与资产状态。 */
export const SAMPLE_MEDIA = {
  name: "e2e-sample.mp4",
  mimeType: "video/mp4",
  buffer: Buffer.from("yaomu-e2e-fixture"),
};

/** 小型合成音频：验证克隆页导入链路，不依赖真实模型。 */
export const SAMPLE_AUDIO = {
  name: "e2e-sample.wav",
  mimeType: "audio/wav",
  buffer: Buffer.from("RIFF0000WAVEfmt "),
};

/** 小型合成 SRT：验证字幕导入/翻译链路，不依赖真实模型。 */
export const SAMPLE_SRT = {
  name: "e2e-sample.srt",
  mimeType: "application/x-subrip",
  buffer: Buffer.from("1\n00:00:00,000 --> 00:00:01,000\nHello\n\n2\n00:00:01,200 --> 00:00:02,000\nWorld\n"),
};

/** 文件名带目标语言码的译文字幕：验证克隆页按文件名识别语言（zh-CN 须作为独立 token）。 */
export const SAMPLE_TRANSLATED_SRT = {
  name: "e2e-sample.zh-CN.srt",
  mimeType: "application/x-subrip",
  buffer: SAMPLE_SRT.buffer,
};

/** 通过 Pipeline 的隐藏 file input 上传，并等待资产与导入提示出现。 */
export async function importSampleMedia(page: Page): Promise<void> {
  await page.locator('input[type="file"]').setInputFiles(SAMPLE_MEDIA);
  await expect(page.getByRole("status").filter({ hasText: "1 file imported" })).toBeVisible();
  await expect(page.locator("li.asset").filter({ hasText: SAMPLE_MEDIA.name })).toBeVisible();
}

/**
 * 让下一次字幕保存挂起，直到用例显式调用 `releaseSaveSubtitle`。
 * 用可释放的 promise 屏障构造保存中状态，不引入固定延时；仅 DEV 生效。
 */
export async function seedHeldSaveSubtitle(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    const control = (scope.__yaomuMock ??= {});
    control.saveSubtitleGate = new Promise<void>((resolve) => {
      control.releaseSaveSubtitle = resolve;
    });
  });
}

/** 释放由 seedHeldSaveSubtitle 挂起的保存请求。 */
export async function releaseSaveSubtitle(page: Page): Promise<void> {
  await page.evaluate(() => {
    const control = (
      window as Window & {
        __yaomuMock?: { releaseSaveSubtitle?: () => void };
      }
    ).__yaomuMock;
    control?.releaseSaveSubtitle?.();
  });
}

/** 让下一次字幕保存以 409 冲突失败（仅 DEV 生效）。 */
export async function seedSaveSubtitleFailure(page: Page, failure: "conflict"): Promise<void> {
  await page.addInitScript((value) => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    scope.__yaomuMock = { ...(scope.__yaomuMock ?? {}), saveSubtitleFailure: value };
  }, failure);
}

/** 读取 mock 侧实际收到的审核批准请求数。 */
export async function readApproveCalls(page: Page): Promise<number> {
  return page.evaluate(() => {
    const control = (
      window as Window & { __yaomuMock?: { approveReviewInputs?: unknown[] } }
    ).__yaomuMock;
    return control?.approveReviewInputs?.length ?? 0;
  });
}

// ---------------------------------------------------------------------------
// 导出探针（仅 DEV mock）
// ---------------------------------------------------------------------------

export interface ExportInputProbe {
  kind?: string;
  format?: string;
  language?: string;
  outputDir?: string;
  namingTemplate?: string;
  inputRevisionId?: string;
  bilingual?: boolean;
}

/** 让 mock 导出任务进入 FAILED；验证 UI 不生成假文件、不宣称成功。 */
export async function seedExportFailure(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    const control = (scope.__yaomuMock ??= {});
    control.exportFailure = true;
  });
}

/** 读取 mock 侧最后一次 `POST /exports` 的请求字段。 */
export async function readLastExportInput(page: Page): Promise<ExportInputProbe | null> {
  return page.evaluate<ExportInputProbe | null>(() => {
    const control = (
      window as Window & { __yaomuMock?: { lastExportInput?: unknown } }
    ).__yaomuMock;
    return (control?.lastExportInput ?? null) as ExportInputProbe | null;
  });
}

/** 读取 mock 侧全部导出请求（批量导出逐项断言）。 */
export async function readExportInputs(page: Page): Promise<ExportInputProbe[]> {
  return page.evaluate<ExportInputProbe[]>(() => {
    const control = (
      window as Window & { __yaomuMock?: { exportInputs?: unknown } }
    ).__yaomuMock;
    return Array.isArray(control?.exportInputs)
      ? (control.exportInputs as ExportInputProbe[])
      : [];
  });
}

/** 读取 mock 侧实际受理的导出请求数。 */
export async function readExportCalls(page: Page): Promise<number> {
  return page.evaluate(() => {
    const control = (window as Window & { __yaomuMock?: { exportCalls?: number } })
      .__yaomuMock;
    return control?.exportCalls ?? 0;
  });
}

// ---------------------------------------------------------------------------
// 审核当前绑定 fixture（仅 DEV mock）
//
// currentBindings 是 Core 的只读投影；这里显式提供是为了构造旧/新混合、空 language
// 多语种、缺字段旧服务等控制面场景。浏览器测试只证明 UI 选择/网关行为，不证明
// Core 摘要算法与真实授权。
// ---------------------------------------------------------------------------

export interface ReviewBindingFixture {
  language: string;
  sourceKind: "translation_revision" | "delivery_revision" | "delivery_mix";
  sourceId: string;
}

export interface ReviewGateFixture {
  id: string;
  runId: string;
  type: "translation" | "delivery";
  language?: string;
  revisionDigest: string;
  status?: "pending" | "approved";
  confirmedAt?: string;
  /** 缺省表示旧 Core 未提供投影（UI 必须 fail closed）。 */
  currentBindings?: ReviewBindingFixture[];
}

/** 用显式 fixture 替换项目审核列表；fixture 的 currentBindings 原样返回，不重新投影。 */
export async function seedReviewFixtures(
  page: Page,
  fixtures: ReviewGateFixture[],
): Promise<void> {
  await page.addInitScript((value) => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    scope.__yaomuMock = { ...(scope.__yaomuMock ?? {}), reviewFixtures: value };
  }, fixtures);
}

export interface ReviewScenarioOptions {
  /** 覆盖 Run 目标语种，用于多语种交付门槛选择。 */
  languages?: string[];
  /** 覆盖 Run 状态：PAUSED 允许登记审核，终态拒绝新批准但保留已审导出例外。 */
  runStatus?: string;
  /** 批准成功后的权威重读失败，验证 UI 不假装已同步。 */
  refreshFailure?: boolean;
}

/** 覆盖审核相关 Run 字段；需在首次导航前调用。 */
export async function seedReviewScenario(
  page: Page,
  options: ReviewScenarioOptions,
): Promise<void> {
  await page.addInitScript((value) => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    const control = (scope.__yaomuMock ??= {});
    if (value.languages) control.reviewRunLanguages = value.languages;
    if (value.runStatus) control.runStatusOverride = value.runStatus;
    if (value.refreshFailure) control.reviewRefreshFailure = true;
  }, options);
}

/** 让审核列表读取持续失败；验证 UI 保持明确未知态而不是当作“无门槛”。 */
export async function seedReviewReadFailure(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    const control = (scope.__yaomuMock ??= {});
    control.reviewReadFailure = true;
  });
}

/** 挂起下一次审核列表读取；释放后返回调用时捕获的旧快照。 */
export async function holdReviewListRead(page: Page): Promise<void> {
  await page.evaluate(() => {
    const control = (
      window as Window & { __yaomuMock?: Record<string, unknown> }
    ).__yaomuMock;
    if (!control) return;
    control.reviewListGate = new Promise<void>((resolve) => {
      control.releaseReviewList = resolve;
    });
  });
}

export async function releaseReviewListRead(page: Page): Promise<void> {
  await page.evaluate(() => {
    const control = (
      window as Window & { __yaomuMock?: { releaseReviewList?: () => void } }
    ).__yaomuMock;
    control?.releaseReviewList?.();
  });
}

/** 挂起下一次批准回包；服务端状态已变更，UI 回包在释放后才到达。 */
export async function holdApproveReview(page: Page): Promise<void> {
  await page.evaluate(() => {
    const control = (
      window as Window & { __yaomuMock?: Record<string, unknown> }
    ).__yaomuMock;
    if (!control) return;
    control.approveReviewGate = new Promise<void>((resolve) => {
      control.releaseApproveReview = resolve;
    });
  });
}

export async function releaseApproveReview(page: Page): Promise<void> {
  await page.evaluate(() => {
    const control = (
      window as Window & { __yaomuMock?: { releaseApproveReview?: () => void } }
    ).__yaomuMock;
    control?.releaseApproveReview?.();
  });
}

export interface ApproveReviewProbe {
  reviewId: string;
  revisionDigest: string;
  type: string;
  currentBinding?: ReviewBindingFixture;
}

/** 读取 mock 侧实际收到的批准入参（含 currentBinding tuple）。 */
export async function readApproveInputs(page: Page): Promise<ApproveReviewProbe[]> {
  return page.evaluate(() => {
    const control = (
      window as Window & { __yaomuMock?: { approveReviewInputs?: unknown[] } }
    ).__yaomuMock;
    return (control?.approveReviewInputs ?? []) as ApproveReviewProbe[];
  });
}

// ---------------------------------------------------------------------------
// 桌面 runtime 就绪桥 fixture（仅 E2E；业务数据仍由显式 demo mock 提供）
//
// 浏览器没有 preload，用例在首次导航前注入 window.yaomu.runtime，模拟契约
// contracts/desktop-runtime.md 的 snapshot/onChanged/retry。所有等待用可释放的
// promise 屏障，不用固定延时；业务 API 仍是测试夹具数据，不把 fake 状态当生产数据。
// ---------------------------------------------------------------------------

export type RuntimePhaseFixture =
  | "preparing"
  | "starting"
  | "ready"
  | "restarting"
  | "failed"
  | "stopping"
  | "stopped";

export type RuntimeReasonFixture =
  | "startup_failed"
  | "core_exited"
  | "cleanup_unconfirmed"
  | "handshake_failed"
  | "protocol_mismatch"
  | "workspace_mismatch";

export interface RuntimeSnapshotFixture {
  schemaVersion: 1;
  revision: number;
  generation: number;
  phase: RuntimePhaseFixture;
  reason: RuntimeReasonFixture | null;
  canRetry: boolean;
}

export interface RuntimeBridgeFixtureOptions {
  initial: RuntimeSnapshotFixture;
  /** true 时 getSnapshot 挂起，直到 releaseRuntimeSnapshot；返回调用时捕获的旧快照。 */
  holdSnapshot?: boolean;
  /** true 时 retry 挂起，直到 releaseRuntimeRetry。 */
  holdRetry?: boolean;
  /** retry 返回的快照；缺省返回当前快照。 */
  retrySnapshot?: RuntimeSnapshotFixture;
}

export function runtimeSnapshot(
  partial: Partial<RuntimeSnapshotFixture> &
    Pick<RuntimeSnapshotFixture, "revision" | "phase">,
): RuntimeSnapshotFixture {
  return {
    schemaVersion: 1,
    generation: 0,
    reason: null,
    canRetry: false,
    ...partial,
  };
}

/** 在首次导航前注入 runtime 桥；与 seedMockControl 共用同一个控制对象。 */
export async function seedRuntimeBridge(
  page: Page,
  options: RuntimeBridgeFixtureOptions,
): Promise<void> {
  await page.addInitScript((opts) => {
    const scope = window as unknown as {
      yaomu?: Record<string, unknown>;
      __yaomuMock?: Record<string, unknown>;
    };
    const control = (scope.__yaomuMock ??= {});
    const listeners = new Set<(snapshot: unknown) => void>();
    let latest: unknown = opts.initial;
    let releaseSnapshot: (() => void) | undefined;
    let releaseRetry: (() => void) | undefined;
    const snapshotGate = new Promise<void>((resolve) => {
      releaseSnapshot = resolve;
    });
    const retryGate = new Promise<void>((resolve) => {
      releaseRetry = resolve;
    });

    const runtime = {
      onChanged(callback: (snapshot: unknown) => void) {
        listeners.add(callback);
        return () => {
          listeners.delete(callback);
        };
      },
      async getSnapshot() {
        // 调用时捕获：释放后仍返回旧快照，验证旧回包不覆盖新通知。
        const captured = latest;
        if (opts.holdSnapshot) await snapshotGate;
        return { ok: true as const, data: captured };
      },
      async retry() {
        control.runtimeRetryCalls = (Number(control.runtimeRetryCalls) || 0) + 1;
        if (opts.holdRetry) await retryGate;
        return { ok: true as const, data: opts.retrySnapshot ?? latest };
      },
    };

    scope.yaomu = scope.yaomu ?? {};
    scope.yaomu.runtime = runtime;
    control.runtimeEmit = (next: unknown) => {
      latest = next;
      for (const listener of listeners) listener(next);
    };
    control.runtimeReleaseSnapshot = () => releaseSnapshot?.();
    control.runtimeReleaseRetry = () => releaseRetry?.();
  }, options);
}

/** 广播一次 Main 侧快照变化（revision 由用例显式提供，不使用本地时间戳）。 */
export async function emitRuntimeSnapshot(
  page: Page,
  snapshot: RuntimeSnapshotFixture,
): Promise<void> {
  await page.evaluate((next) => {
    const control = (
      window as unknown as {
        __yaomuMock?: { runtimeEmit?: (snapshot: unknown) => void };
      }
    ).__yaomuMock;
    control?.runtimeEmit?.(next);
  }, snapshot);
}

export async function releaseRuntimeSnapshot(page: Page): Promise<void> {
  await page.evaluate(() => {
    const control = (
      window as unknown as {
        __yaomuMock?: { runtimeReleaseSnapshot?: () => void };
      }
    ).__yaomuMock;
    control?.runtimeReleaseSnapshot?.();
  });
}

export async function releaseRuntimeRetry(page: Page): Promise<void> {
  await page.evaluate(() => {
    const control = (
      window as unknown as {
        __yaomuMock?: { runtimeReleaseRetry?: () => void };
      }
    ).__yaomuMock;
    control?.runtimeReleaseRetry?.();
  });
}

export async function readRuntimeRetryCalls(page: Page): Promise<number> {
  return page.evaluate(() => {
    const control = (
      window as unknown as { __yaomuMock?: { runtimeRetryCalls?: number } }
    ).__yaomuMock;
    return control?.runtimeRetryCalls ?? 0;
  });
}

/** 测试夹具的业务调用记录（ui/src/api/e2eSource.enabled.ts 仅在测试构建记录）。 */
export async function readBusinessCalls(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const control = (
      window as unknown as { __yaomuMock?: { businessCalls?: string[] } }
    ).__yaomuMock;
    return control?.businessCalls ? [...control.businessCalls] : [];
  });
}

/** 挂起下一次 getProject（晚释放返回旧快照）；预置初始项目名。 */
export async function seedProjectReadGate(page: Page, initialName: string): Promise<void> {
  await page.addInitScript((name) => {
    const scope = window as unknown as {
      __yaomuMock?: Record<string, unknown>;
    };
    const control = (scope.__yaomuMock ??= {});
    control.projectNameFixtures = { "proj-demo": name };
    control.getProjectGate = new Promise<void>((resolve) => {
      control.releaseGetProject = resolve;
    });
  }, initialName);
}

export async function setProjectNameFixture(page: Page, name: string): Promise<void> {
  await page.evaluate((value) => {
    const control = (
      window as unknown as {
        __yaomuMock?: { projectNameFixtures?: Record<string, string> };
      }
    ).__yaomuMock;
    if (control) control.projectNameFixtures = { "proj-demo": value };
  }, name);
}

/** 用可控摘要替换项目列表，用于排序/筛选等列表级断言（不读取详情）。 */
export async function seedProjectList(
  page: Page,
  items: Array<Record<string, unknown>>,
): Promise<void> {
  await page.addInitScript((value) => {
    const scope = window as unknown as {
      __yaomuMock?: Record<string, unknown>;
    };
    const control = (scope.__yaomuMock ??= {});
    control.projectListOverride = value;
  }, items);
}

export async function releaseProjectReadGate(page: Page): Promise<void> {
  await page.evaluate(() => {
    const control = (
      window as unknown as { __yaomuMock?: { releaseGetProject?: () => void } }
    ).__yaomuMock;
    control?.releaseGetProject?.();
  });
}

/** 在页面运行中挂起下一次 getProject；用于“读取在途时用户开始编辑”的竞态。 */
export async function holdNextProjectRead(page: Page): Promise<void> {
  await page.evaluate(() => {
    const control = (
      window as unknown as { __yaomuMock?: Record<string, unknown> }
    ).__yaomuMock;
    if (!control) return;
    control.getProjectGate = new Promise<void>((resolve) => {
      control.releaseGetProject = resolve;
    });
  });
}

/** 已发起的 getProject 次数（mock 侧计数），用于等待可释放读取真正开始。 */
export async function readProjectReadCalls(page: Page): Promise<number> {
  return page.evaluate(() => {
    const control = (
      window as unknown as { __yaomuMock?: { getProjectCalls?: number } }
    ).__yaomuMock;
    return control?.getProjectCalls ?? 0;
  });
}

// ---------------------------------------------------------------------------
// 任务进度/连接通知/执行日志 fixture（仅测试夹具）
//
// 与 mock.ts 的 MockControl 对齐：可释放读取屏障返回调用时捕获的旧快照；
// 所有断言基于可观察的 DOM/计数，不引入固定延时。
// ---------------------------------------------------------------------------

interface TaskProgressFixtureInput {
  taskId: string;
  projectId?: string;
  stateVersion: number;
  stepId: string;
  attempt: number;
  epoch: number | string;
  progress: number;
}

export async function emitTaskProgress(
  page: Page,
  input: TaskProgressFixtureInput,
): Promise<void> {
  await page.evaluate((value) => {
    const control = (
      window as unknown as {
        __yaomuMock?: { emitTaskProgress?: (input: unknown) => void };
      }
    ).__yaomuMock;
    control?.emitTaskProgress?.(value);
  }, input);
}

/** 注入任意事件形状（验证消费者对非法 schema/seq/身份 fail-closed）。 */
export async function emitRawEvent(page: Page, event: unknown): Promise<void> {
  await page.evaluate((value) => {
    const control = (
      window as unknown as {
        __yaomuMock?: { emitRawEvent?: (input: unknown) => void };
      }
    ).__yaomuMock;
    control?.emitRawEvent?.(value);
  }, event);
}

/** 持久关键事件（会进入演示回放日志）。 */
export async function emitDomainEvent(
  page: Page,
  type: string,
  payload: Record<string, unknown> = {},
): Promise<void> {
  await page.evaluate(
    ({ eventType, data }) => {
      const control = (
        window as unknown as {
          __yaomuMock?: {
            emitDomainEvent?: (type: string, payload?: Record<string, unknown>) => void;
          };
        }
      ).__yaomuMock;
      control?.emitDomainEvent?.(eventType, data);
    },
    { eventType: type, data: payload },
  );
}

export async function setMockTask(
  page: Page,
  taskId: string,
  patch: Record<string, unknown>,
): Promise<void> {
  await page.evaluate(
    ({ id, value }) => {
      const control = (
        window as unknown as {
          __yaomuMock?: {
            setTask?: (taskId: string, patch: Record<string, unknown>) => void;
          };
        }
      ).__yaomuMock;
      control?.setTask?.(id, value);
    },
    { id: taskId, value: patch },
  );
}

/** 追加一条演示任务（模拟全局 list 捕获后、项目快照窗口内新建任务）。 */
export async function addMockTask(page: Page, task: Record<string, unknown>): Promise<void> {
  await page.evaluate((value) => {
    const control = (
      window as unknown as {
        __yaomuMock?: { addTask?: (task: Record<string, unknown>) => void };
      }
    ).__yaomuMock;
    control?.addTask?.(value);
  }, task);
}

/** 模拟 Main 成功 SSE 连接通知（初连/自动重连），不产生业务事件。 */
export async function emitEventStreamConnected(page: Page): Promise<void> {
  await page.evaluate(() => {
    const control = (
      window as unknown as { __yaomuMock?: { emitEventStreamConnected?: () => void } }
    ).__yaomuMock;
    control?.emitEventStreamConnected?.();
  });
}

/** 模拟已退订旧流的迟到事件/连接通知（验证 UI 代次守卫）。 */
export async function emitStaleEventStreamEvent(page: Page, event: unknown): Promise<void> {
  await page.evaluate((value) => {
    const control = (
      window as unknown as {
        __yaomuMock?: { emitStaleSubscriptionEvent?: (event: unknown) => void };
      }
    ).__yaomuMock;
    control?.emitStaleSubscriptionEvent?.(value);
  }, event);
}

export async function emitStaleEventStreamConnected(page: Page): Promise<void> {
  await page.evaluate(() => {
    const control = (
      window as unknown as {
        __yaomuMock?: { emitStaleSubscriptionConnected?: () => void };
      }
    ).__yaomuMock;
    control?.emitStaleSubscriptionConnected?.();
  });
}

type MockGateKey =
  | "listTasksGate"
  | "getSnapshotGate"
  | "getTaskGate"
  | "executionLogGate";

async function holdMockGate(page: Page, gate: MockGateKey): Promise<void> {
  await page.evaluate((key) => {
    const control = (
      window as unknown as { __yaomuMock?: Record<string, unknown> }
    ).__yaomuMock;
    if (!control) return;
    // 调用时捕获的数据在释放后返回；同一屏障可被多个读取共享。
    control[key] = new Promise<void>((resolve) => {
      const base = String(key).replace("Gate", "");
      const releaseKey = `release${base[0].toUpperCase()}${base.slice(1)}`;
      control[releaseKey] = resolve;
    });
  }, gate);
}

async function releaseMockGate(page: Page, release: string): Promise<void> {
  await page.evaluate((key) => {
    const control = (
      window as unknown as { __yaomuMock?: Record<string, unknown> }
    ).__yaomuMock;
    const callback = control?.[key];
    if (typeof callback === "function") (callback as () => void)();
  }, release);
}

export async function holdListTasksRead(page: Page): Promise<void> {
  await holdMockGate(page, "listTasksGate");
}

export async function releaseListTasksRead(page: Page): Promise<void> {
  await releaseMockGate(page, "releaseListTasks");
}

export async function holdSnapshotRead(page: Page): Promise<void> {
  await holdMockGate(page, "getSnapshotGate");
}

export async function releaseSnapshotRead(page: Page): Promise<void> {
  await releaseMockGate(page, "releaseGetSnapshot");
}

export async function holdTaskDetailRead(page: Page): Promise<void> {
  await holdMockGate(page, "getTaskGate");
}

export async function releaseTaskDetailRead(page: Page): Promise<void> {
  await releaseMockGate(page, "releaseGetTask");
}

export async function holdExecutionLogRead(page: Page): Promise<void> {
  await holdMockGate(page, "executionLogGate");
}

export async function releaseExecutionLogRead(page: Page): Promise<void> {
  await releaseMockGate(page, "releaseExecutionLog");
}

type MockCallCounters = {
  listTasksCalls?: number;
  getSnapshotCalls?: number;
  getTaskCalls?: number;
  executionLogCalls?: number;
  eventSubscriptionCount?: number;
  connectedNotificationCount?: number;
  staleConnectedCalls?: number;
};

export async function readMockCallCounters(page: Page): Promise<MockCallCounters> {
  return page.evaluate(() => {
    const control = (
      window as unknown as { __yaomuMock?: MockCallCounters }
    ).__yaomuMock;
    if (!control) return {};
    const {
      listTasksCalls,
      getSnapshotCalls,
      getTaskCalls,
      executionLogCalls,
      eventSubscriptionCount,
      connectedNotificationCount,
      staleConnectedCalls,
    } = control;
    return {
      listTasksCalls,
      getSnapshotCalls,
      getTaskCalls,
      executionLogCalls,
      eventSubscriptionCount,
      connectedNotificationCount,
      staleConnectedCalls,
    };
  });
}

/** 清空测试夹具业务调用记录，便于按窗口断言请求数。 */
export async function clearBusinessCalls(page: Page): Promise<void> {
  await page.evaluate(() => {
    const control = (
      window as unknown as { __yaomuMock?: { businessCalls?: string[] } }
    ).__yaomuMock;
    if (control) control.businessCalls = [];
  });
}

/** 追加一条执行过程日志实时事件（新 attempt 会替换旧缓存）。 */
export async function appendExecutionLog(
  page: Page,
  taskId: string,
  attempt: number,
  stream: "runner" | "stdout" | "stderr",
  line: string,
): Promise<void> {
  await page.evaluate(
    ({ id, attemptNo, streamName, text }) => {
      const control = (
        window as unknown as {
          __yaomuMock?: {
            appendExecutionLog?: (
              taskId: string,
              attempt: number,
              stream: string,
              line: string,
            ) => void;
          };
        }
      ).__yaomuMock;
      control?.appendExecutionLog?.(id, attemptNo, streamName, text);
    },
    { id: taskId, attemptNo: attempt, streamName: stream, text: line },
  );
}

export async function readLastExecutionLogTaskId(page: Page): Promise<string> {
  return page.evaluate(() => {
    const control = (
      window as unknown as { __yaomuMock?: { lastExecutionLogTaskId?: string } }
    ).__yaomuMock;
    return control?.lastExecutionLogTaskId ?? "";
  });
}

// ---------------------------------------------------------------------------
// 受守卫的应用更新安装桥 fixture（仅 E2E）
//
// 浏览器没有 preload；用例在首次导航前注入 window.yaomu.update 的有限状态桥，
// 模拟 contracts/app-update-installation.md 的 snapshot/install/cancel/onInstallState。
// 浏览器测试只证明 UI 消费与安全文案，不证明真实 updater、签名或安装行为。
// ---------------------------------------------------------------------------

export type UpdateInstallPhaseFixture =
  | "idle"
  | "waiting"
  | "preparing"
  | "stopping"
  | "installing"
  | "blocked";

export type UpdateInstallReasonFixture =
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

export interface UpdateInstallSnapshotFixture {
  schemaVersion: 1;
  revision: number;
  phase: UpdateInstallPhaseFixture;
  reason: UpdateInstallReasonFixture | null;
  version: string | null;
  canCancel: boolean;
}

export interface UpdateBridgeFixtureOptions {
  /** getInstallState 的初始/当前快照；缺省 idle。 */
  initial?: UpdateInstallSnapshotFixture;
  /** install() 返回的状态；缺省返回当前快照。 */
  installResult?: UpdateInstallSnapshotFixture;
  /** cancelInstall() 返回的状态；缺省 idle。 */
  cancelResult?: UpdateInstallSnapshotFixture;
  /** true 时 getInstallState 挂起，直到 releaseUpdateState。 */
  holdState?: boolean;
  /** true 时 getInstallState 返回畸形快照，验证 UI fail closed。 */
  malformedState?: boolean;
  /** true 时模拟旧 preload：没有 getInstallState/cancelInstall/onInstallState。 */
  legacy?: boolean;
}

export function updateInstallSnapshot(
  partial: Partial<UpdateInstallSnapshotFixture> &
    Pick<UpdateInstallSnapshotFixture, "revision" | "phase">,
): UpdateInstallSnapshotFixture {
  return {
    schemaVersion: 1,
    reason: null,
    version: null,
    canCancel: partial.phase === "waiting",
    ...partial,
  };
}

export async function seedUpdateBridge(
  page: Page,
  options: UpdateBridgeFixtureOptions = {},
): Promise<void> {
  await page.addInitScript((opts) => {
    const scope = window as unknown as {
      yaomu?: Record<string, unknown>;
      __yaomuMock?: Record<string, unknown>;
    };
    const control = (scope.__yaomuMock ??= {});
    const idle = {
      schemaVersion: 1,
      revision: 1,
      phase: "idle",
      reason: null,
      version: null,
      canCancel: false,
    };
    let latest: unknown = opts.initial ?? idle;
    const installListeners = new Set<(snapshot: unknown) => void>();
    const statusListeners = new Set<(status: string) => void>();
    const availableListeners = new Set<(info: unknown) => void>();
    const progressListeners = new Set<(progress: unknown) => void>();
    const downloadedListeners = new Set<(info: unknown) => void>();
    const errorListeners = new Set<(message: string) => void>();
    let releaseState: (() => void) | undefined;
    const stateGate = new Promise<void>((resolve) => {
      releaseState = resolve;
    });
    const count = (key: string) => {
      control[key] = (Number(control[key]) || 0) + 1;
    };
    const ok = (data: unknown) => ({ ok: true as const, data });

    const update: Record<string, unknown> = {
      check: async () => {
        count("updateCheckCalls");
        for (const listener of statusListeners) listener("checking");
        return ok({});
      },
      download: async () => {
        count("updateDownloadCalls");
        return ok({});
      },
      install: async () => {
        count("updateInstallCalls");
        latest = opts.installResult ?? latest;
        return ok(latest);
      },
      onStatus(callback: (status: string) => void) {
        statusListeners.add(callback);
        return () => statusListeners.delete(callback);
      },
      onAvailable(callback: (info: unknown) => void) {
        availableListeners.add(callback);
        return () => availableListeners.delete(callback);
      },
      onNotAvailable() {
        return () => undefined;
      },
      onProgress(callback: (progress: unknown) => void) {
        progressListeners.add(callback);
        return () => progressListeners.delete(callback);
      },
      onDownloaded(callback: (info: unknown) => void) {
        downloadedListeners.add(callback);
        return () => downloadedListeners.delete(callback);
      },
      onError(callback: (message: string) => void) {
        errorListeners.add(callback);
        return () => errorListeners.delete(callback);
      },
    };
    if (!opts.legacy) {
      update.getInstallState = async () => {
        count("updateGetStateCalls");
        const captured = latest;
        if (opts.holdState) await stateGate;
        if (opts.malformedState) {
          return ok({ schemaVersion: 1, revision: 1, phase: "waiting" });
        }
        return ok(captured);
      };
      update.cancelInstall = async () => {
        count("updateCancelCalls");
        latest = opts.cancelResult ?? idle;
        return ok(latest);
      };
      update.onInstallState = (callback: (snapshot: unknown) => void) => {
        installListeners.add(callback);
        return () => installListeners.delete(callback);
      };
    }

    scope.yaomu = scope.yaomu ?? {};
    scope.yaomu.platform = "browser";
    scope.yaomu.update = update;
    // 完整业务代理，避免 About 的只读加载把页面带进 unavailable 横幅。
    const bridge = new Proxy(
      {},
      {
        get(_target, property) {
          if (property === "subscribeEvents") return () => () => undefined;
          return async () => ({ ok: true, data: {} });
        },
      },
    );
    scope.yaomu.api = bridge;

    control.updateEmitInstallState = (next: unknown) => {
      latest = next;
      for (const listener of installListeners) listener(next);
    };
    control.updateEmitDownloaded = (info: { version: string }) => {
      for (const listener of downloadedListeners) listener(info);
    };
    control.updateEmitAvailable = (info: { version: string }) => {
      for (const listener of availableListeners) listener(info);
    };
    control.updateEmitProgress = (value: unknown) => {
      for (const listener of progressListeners) listener(value);
    };
    control.updateEmitStatus = (status: string) => {
      for (const listener of statusListeners) listener(status);
    };
    control.updateEmitError = (message: string) => {
      for (const listener of errorListeners) listener(message);
    };
    control.updateReleaseState = () => releaseState?.();
  }, options);
}

/** 推送一次宿主安装快照（revision 由用例显式提供，不使用本地时间戳）。 */
export async function emitUpdateInstallState(
  page: Page,
  snapshot: UpdateInstallSnapshotFixture,
): Promise<void> {
  await page.evaluate((next) => {
    const control = (
      window as unknown as {
        __yaomuMock?: { updateEmitInstallState?: (snapshot: unknown) => void };
      }
    ).__yaomuMock;
    control?.updateEmitInstallState?.(next);
  }, snapshot);
}

/** 触发 update-downloaded，使 UI 进入 downloaded 并显示安装入口。 */
export async function emitUpdateDownloaded(page: Page, version: string): Promise<void> {
  await page.evaluate((value) => {
    const control = (
      window as unknown as {
        __yaomuMock?: { updateEmitDownloaded?: (info: { version: string }) => void };
      }
    ).__yaomuMock;
    control?.updateEmitDownloaded?.({ version: value });
  }, version);
}

/** 触发 update-available，使 UI 进入 available 并显示顶栏下载入口。 */
export async function emitUpdateAvailable(page: Page, version: string): Promise<void> {
  await page.evaluate((value) => {
    const control = (
      window as unknown as {
        __yaomuMock?: { updateEmitAvailable?: (info: { version: string }) => void };
      }
    ).__yaomuMock;
    control?.updateEmitAvailable?.({ version: value });
  }, version);
}

/** 触发 download-progress，使 UI 进入 downloading。 */
export async function emitUpdateProgress(page: Page, progress: unknown): Promise<void> {
  await page.evaluate((value) => {
    const control = (
      window as unknown as {
        __yaomuMock?: { updateEmitProgress?: (progress: unknown) => void };
      }
    ).__yaomuMock;
    control?.updateEmitProgress?.(value);
  }, progress);
}

/** 触发 update:status（checking/idle/available），验证状态不倒退。 */
export async function emitUpdateStatus(page: Page, status: string): Promise<void> {
  await page.evaluate((value) => {
    const control = (
      window as unknown as {
        __yaomuMock?: { updateEmitStatus?: (status: string) => void };
      }
    ).__yaomuMock;
    control?.updateEmitStatus?.(value);
  }, status);
}

/** 触发 update:error，验证已下载状态不会被一次检查错误清掉。 */
export async function emitUpdateError(page: Page, message = "update_failed"): Promise<void> {
  await page.evaluate((value) => {
    const control = (
      window as unknown as {
        __yaomuMock?: { updateEmitError?: (message: string) => void };
      }
    ).__yaomuMock;
    control?.updateEmitError?.(value);
  }, message);
}

export async function releaseUpdateState(page: Page): Promise<void> {
  await page.evaluate(() => {
    const control = (
      window as unknown as { __yaomuMock?: { updateReleaseState?: () => void } }
    ).__yaomuMock;
    control?.updateReleaseState?.();
  });
}

export async function readUpdateCalls(
  page: Page,
): Promise<{ install: number; cancel: number; check: number; download: number; getState: number }> {
  return page.evaluate(() => {
    const control = (
      window as unknown as {
        __yaomuMock?: {
          updateInstallCalls?: number;
          updateCancelCalls?: number;
          updateCheckCalls?: number;
          updateDownloadCalls?: number;
          updateGetStateCalls?: number;
        };
      }
    ).__yaomuMock;
    return {
      install: control?.updateInstallCalls ?? 0,
      cancel: control?.updateCancelCalls ?? 0,
      check: control?.updateCheckCalls ?? 0,
      download: control?.updateDownloadCalls ?? 0,
      getState: control?.updateGetStateCalls ?? 0,
    };
  });
}
