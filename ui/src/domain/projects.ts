import type { ProjectSummary, RunStatus } from "./types";

/** 项目列表排序：最近使用优先，或按创建时间升序（最新在最后）。 */
export type ProjectSortMode = "recent" | "created";

/** 项目类型筛选的 7 个业务分类。 */
export type ProjectTypeKey =
  | "pipeline"
  | "erase"
  | "translate"
  | "clone"
  | "extract"
  | "separation"
  | "custom";

/** 项目列表的四组状态。内部 WAITING_REVIEW 归入 completed。 */
export type ProjectStatusBucket = "pending" | "running" | "completed" | "failed";

/**
 * 项目卡片统一排序：最近打开/更新优先，使用稳定 tie-breaker 避免两个页面
 * 因加载时序不同出现不一致顺序。
 */
export function sortProjectsByRecent(items: ProjectSummary[]): ProjectSummary[] {
  return [...items].sort((a, b) => {
    const recency = (b.lastOpenedAt ?? b.updatedAt).localeCompare(
      a.lastOpenedAt ?? a.updatedAt,
    );
    if (recency !== 0) return recency;
    return a.name.localeCompare(b.name) || a.id.localeCompare(b.id);
  });
}

/** 创建时间升序：最新创建的项目排在最后。 */
export function sortProjectsByCreatedAsc(items: ProjectSummary[]): ProjectSummary[] {
  return [...items].sort((a, b) => {
    const created = a.createdAt.localeCompare(b.createdAt);
    if (created !== 0) return created;
    return a.name.localeCompare(b.name) || a.id.localeCompare(b.id);
  });
}

/**
 * 由项目/Run 的 modeType 推导卡片类型；空值返回 undefined（筛选时不匹配任何具体类型）。
 * 与 ProjectCard 的展示口径共用，避免筛选和卡片标签漂移。
 */
export function projectTypeKeyFromMode(modeType?: string): ProjectTypeKey | undefined {
  if (!modeType) return undefined;
  if (modeType.startsWith("full_pipeline")) return "pipeline";
  if (modeType === "custom_workflow") return "custom";
  if (modeType.startsWith("audio_separate") && !modeType.includes("_compose")) return "separation";
  if (modeType.startsWith("subtitle_erase_dialog")) {
    return modeType.includes("_compose") ? "custom" : "erase";
  }
  if (modeType.startsWith("subtitle_translate")) {
    return modeType.includes("_compose") ? "custom" : "translate";
  }
  if (modeType.startsWith("high_sentiment_tts_clone") || modeType.startsWith("tts_clone")) {
    return modeType.includes("_compose") ? "custom" : "clone";
  }
  if (modeType.startsWith("subtitle_extract_")) {
    return modeType.includes("_compose") ? "custom" : "extract";
  }
  return "custom";
}

/** 项目级四态分桶；WAITING_REVIEW 表示模型完成、等待人工校正，归入 completed。 */
export function projectStatusBucket(status?: RunStatus): ProjectStatusBucket {
  switch (status) {
    case "RUNNING":
    case "PAUSE_REQUESTED":
    case "PAUSED":
      return "running";
    case "WAITING_REVIEW":
    case "SUCCEEDED":
      return "completed";
    case "PARTIAL":
    case "FAILED":
    case "CANCELED":
      return "failed";
    default:
      return "pending";
  }
}
