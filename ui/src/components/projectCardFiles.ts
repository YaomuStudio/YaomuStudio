// 项目卡片下载/预览条目构造：把角色、语言、真实类型与建议文件名收敛到单一纯逻辑。
// 未被 Core 或旧数据证实的信息不猜测：无 role/未知 role 只按真实类型与文件名展示。
import { getLocale, t } from "@/i18n";
import { targetLanguageName } from "@/domain/languages";
import type { Asset, AssetRole } from "@/domain/types";

export type DownloadGroup = "video" | "audio" | "subtitle";
export type CardSimpleType =
  | ""
  | "erase"
  | "separation"
  | "translate"
  | "clone"
  | "extract";

export interface ProjectDownloadItem {
  asset: Asset;
  /** 下载菜单文案（原片 / 原始音频 / 导入字幕 · 文件名…）。 */
  label: string;
  /** 建议保存文件名；同一列表内大小写不敏感唯一。 */
  filename: string;
  /** 预览 tab 文案；与下载条目共用同一资产身份。 */
  previewLabel: string;
  /** 实际扩展名（含点）；用于弹窗右侧展示。 */
  extension: string;
  group: DownloadGroup;
}

interface DownloadMeta {
  label: string;
  stem: string;
  previewLabel: string;
  prefix: "_" | "-";
}

const ROLE_ORDER: AssetRole[] = [
  "original",
  "voice",
  "ambience",
  "clean_video",
  "source_subtitle",
  "translated_subtitle",
  "dub_audio",
  "full_dub_audio",
  "translated_video",
  "final_video",
];

const KNOWN_ROLES = new Set<string>(ROLE_ORDER);

const DOWNLOADABLE_TYPES = new Set<Asset["type"]>([
  "video",
  "audio",
  "audio_track",
  "subtitle",
]);

const ALL_GROUPS: DownloadGroup[] = ["video", "audio", "subtitle"];

function isKnownRole(role: AssetRole | undefined): role is AssetRole {
  return role !== undefined && KNOWN_ROLES.has(role);
}

/** 规范文件名：去掉路径分隔符、Windows 非法字符与控制字符，并清理结尾点/空格。 */
export function fileSafe(name: string): string {
  return name
    .replace(/[\\/:*?"<>|\u0000-\u001f\u007f]/g, "_")
    .replace(/[. ]+$/g, "")
    .trimStart();
}

/** 与 desktop/src/ipc/asset-content.ts 的 MAX_NAME 对齐，避免主进程截断丢掉后缀/扩展名。 */
const MAX_FILENAME_UNITS = 120;
/** 常见文件系统单个文件名 255 字节（UTF-8），中文等多字节名称需同时满足。 */
const MAX_FILENAME_BYTES = 255;
const utf8Encoder = new TextEncoder();

function utf8Bytes(text: string): number {
  return utf8Encoder.encode(text).length;
}

/** 按 UTF-16 长度与 UTF-8 字节双上限裁剪，只保留完整码点。 */
function clampName(text: string, maxUnits: number, maxBytes: number): string {
  if (maxUnits <= 0 || maxBytes <= 0) return "";
  let units = 0;
  let bytes = 0;
  let out = "";
  for (const char of text) {
    const nextUnits = units + char.length;
    const nextBytes = bytes + utf8Bytes(char);
    if (nextUnits > maxUnits || nextBytes > maxBytes) break;
    out += char;
    units = nextUnits;
    bytes = nextBytes;
  }
  return out;
}

/**
 * 组装建议名：扩展名与唯一化后缀优先保留，项目名前缀/stem 只按剩余预算裁剪。
 * 同时满足主进程 basename 后 120 长度上限与文件系统 255 字节上限。
 */
function composeFilename(leading: string, extension: string, suffix: string): string {
  const tail = `${suffix}${extension}`;
  const tailUnits = tail.length;
  const tailBytes = utf8Bytes(tail);
  if (tailUnits <= MAX_FILENAME_UNITS && tailBytes <= MAX_FILENAME_BYTES) {
    const body = clampName(
      leading,
      MAX_FILENAME_UNITS - tailUnits,
      MAX_FILENAME_BYTES - tailBytes,
    ).trimEnd();
    return `${body}${tail}`;
  }
  // 极端情况下扩展名本身超限：先保留唯一后缀，再用剩余预算保留扩展名。
  const safeSuffix = clampName(suffix, MAX_FILENAME_UNITS, MAX_FILENAME_BYTES);
  return `${safeSuffix}${clampName(
    extension,
    MAX_FILENAME_UNITS - safeSuffix.length,
    MAX_FILENAME_BYTES - utf8Bytes(safeSuffix),
  )}`;
}

function splitExtension(name: string): { stem: string; ext: string } {
  const index = name.lastIndexOf(".");
  if (index > 0) return { stem: name.slice(0, index), ext: name.slice(index) };
  return { stem: name, ext: "" };
}

function safeStemOf(name: string): string {
  const safe = fileSafe(name);
  return splitExtension(safe).stem || safe || t("workbench.card.dl.typeFile");
}

function assetExtension(asset: Asset, role: AssetRole | undefined): string {
  const actual = splitExtension(fileSafe(asset.name)).ext;
  if (actual) return actual;
  if (asset.type === "subtitle" || role?.endsWith("_subtitle")) return ".srt";
  if (
    asset.type === "audio" ||
    asset.type === "audio_track" ||
    role === "voice" ||
    role === "ambience" ||
    role === "dub_audio" ||
    role === "full_dub_audio"
  ) {
    return ".wav";
  }
  return ".mp4";
}

function typeLabel(type: Asset["type"]): string {
  if (type === "video") return t("workbench.card.dl.typeVideo");
  if (type === "audio" || type === "audio_track") return t("workbench.card.dl.typeAudio");
  if (type === "subtitle") return t("workbench.card.dl.typeSubtitle");
  return t("workbench.card.dl.typeFile");
}

/** 无可信 role 的资产：只展示真实类型 + 原文件名，不推测人声/环境/语种。 */
function unknownMeta(asset: Asset): DownloadMeta {
  const label = `${typeLabel(asset.type)} · ${asset.name}`;
  return {
    label,
    stem: `${typeLabel(asset.type)}_${safeStemOf(asset.name)}`,
    previewLabel: label,
    prefix: "_",
  };
}

function metaFor(asset: Asset): DownloadMeta {
  const role = asset.role;
  if (!isKnownRole(role)) return unknownMeta(asset);
  const lang = asset.language ? targetLanguageName(asset.language, getLocale()) : "";
  switch (role) {
    case "original":
      if (asset.type === "video") {
        return {
          label: t("workbench.card.dl.original"),
          stem: t("workbench.card.dl.original"),
          previewLabel: t("workbench.card.dl.original"),
          prefix: "_",
        };
      }
      if (asset.type === "audio" || asset.type === "audio_track") {
        return {
          label: t("workbench.card.dl.originalAudio"),
          stem: t("workbench.card.dl.originalAudio"),
          previewLabel: t("workbench.card.dl.originalAudio"),
          prefix: "_",
        };
      }
      return unknownMeta(asset);
    case "voice":
      return {
        label: t("workbench.card.dl.voice"),
        stem: t("workbench.card.dl.voice"),
        previewLabel: t("workbench.card.pv.voice"),
        prefix: "-",
      };
    case "ambience":
      return {
        label: t("workbench.card.dl.ambience"),
        stem: t("workbench.card.dl.ambience"),
        previewLabel: t("workbench.card.pv.ambience"),
        prefix: "-",
      };
    case "clean_video":
      return {
        label: t("workbench.card.dl.clean"),
        stem: t("workbench.card.dl.clean"),
        previewLabel: t("workbench.card.dl.clean"),
        prefix: "_",
      };
    case "source_subtitle":
      // 导入 SRT 没有语言与 run_id：保留原文件名，不声称是译文或某个语种。
      if (!lang && !asset.runId) {
        const label = `${t("workbench.card.dl.importedSub")} · ${asset.name}`;
        return {
          label,
          stem: `${t("workbench.card.dl.importedSub")}_${safeStemOf(asset.name)}`,
          previewLabel: label,
          prefix: "_",
        };
      }
      return {
        label: t("workbench.card.dl.originalSub"),
        stem: t("workbench.card.dl.originalSub"),
        previewLabel: t("workbench.card.dl.originalSub"),
        prefix: "_",
      };
    case "translated_subtitle": {
      const label = lang
        ? t("workbench.card.dl.langSubtitle", { language: lang })
        : t("workbench.card.dl.translatedSub");
      return { label, stem: label, previewLabel: label, prefix: "_" };
    }
    case "dub_audio":
      if (lang) {
        return {
          label: t("workbench.card.dl.langDubAudio", { language: lang }),
          stem: t("workbench.card.dl.langDub", { language: lang }),
          previewLabel: t("workbench.card.dl.langDub", { language: lang }),
          prefix: "_",
        };
      }
      return {
        label: t("workbench.card.dl.dubAudioPlain"),
        stem: t("workbench.card.dl.dubAudioPlain"),
        previewLabel: t("workbench.card.dl.dubAudioPlain"),
        prefix: "_",
      };
    case "full_dub_audio":
      if (lang) {
        return {
          label: t("workbench.card.dl.langFullDub", { language: lang }),
          stem: t("workbench.card.dl.langFullDub", { language: lang }),
          previewLabel: t("workbench.card.dl.langFullDub", { language: lang }),
          prefix: "_",
        };
      }
      return {
        label: t("workbench.card.dl.fullDub"),
        stem: t("workbench.card.dl.fullDub"),
        previewLabel: t("workbench.card.dl.fullDub"),
        prefix: "_",
      };
    case "translated_video": {
      const label = lang
        ? t("workbench.card.dl.langVideo", { language: lang })
        : t("workbench.card.dl.translatedVideo");
      return { label, stem: label, previewLabel: label, prefix: "_" };
    }
    case "final_video": {
      const label = lang
        ? t("workbench.card.dl.langFinal", { language: lang })
        : t("workbench.card.dl.final");
      return { label, stem: label, previewLabel: label, prefix: "_" };
    }
    default:
      return unknownMeta(asset);
  }
}

function roleVisible(simpleType: CardSimpleType, role: AssetRole): boolean {
  switch (simpleType) {
    case "erase":
      return ["original", "clean_video", "source_subtitle"].includes(role);
    case "separation":
      return ["original", "voice", "ambience"].includes(role);
    case "translate":
      return ["source_subtitle", "translated_subtitle"].includes(role);
    case "clone":
      return ["voice", "source_subtitle", "translated_subtitle", "dub_audio", "full_dub_audio"].includes(
        role,
      );
    case "extract":
      return ["original", "source_subtitle"].includes(role);
    default:
      return true;
  }
}

function roleGroup(asset: Asset, role: AssetRole | undefined): DownloadGroup {
  if (role === "voice" || role === "ambience" || role === "dub_audio" || role === "full_dub_audio") {
    return "audio";
  }
  if (role === "source_subtitle" || role === "translated_subtitle") return "subtitle";
  if (asset.type === "audio" || asset.type === "audio_track") return "audio";
  if (asset.type === "subtitle") return "subtitle";
  return "video";
}

function roleRank(role: AssetRole | undefined): number {
  if (!isKnownRole(role)) return ROLE_ORDER.length;
  return ROLE_ORDER.indexOf(role);
}

function compareItems(a: { asset: Asset }, b: { asset: Asset }): number {
  const byRole = roleRank(a.asset.role) - roleRank(b.asset.role);
  if (byRole !== 0) return byRole;
  if (a.asset.name !== b.asset.name) return a.asset.name < b.asset.name ? -1 : 1;
  if (a.asset.id === b.asset.id) return 0;
  return a.asset.id < b.asset.id ? -1 : 1;
}

/** 保留扩展名与唯一后缀，在长度/字节预算内重建建议名；比较大小写不敏感。 */
function uniqueFilename(leading: string, extension: string, used: Set<string>): string {
  let index = 1;
  let filename = composeFilename(leading, extension, "");
  while (used.has(filename.toLowerCase())) {
    index += 1;
    filename = composeFilename(leading, extension, ` (${index})`);
  }
  used.add(filename.toLowerCase());
  return filename;
}

export interface BuildDownloadItemsOptions {
  projectName: string;
  assets: readonly Asset[];
  simpleType?: CardSimpleType;
  /**
   * 可见分组；默认全部。已知 role 的可见性另由 simpleType 决定。
   * `original` 与无/未知 role 的文件级条目不受分组过滤；
   * 翻译/克隆不提供原件时由 role 可见性排除。
   */
  allowedGroups?: readonly DownloadGroup[];
}

export function buildProjectDownloadItems(
  options: BuildDownloadItemsOptions,
): ProjectDownloadItem[] {
  const base =
    fileSafe(options.projectName) || t("workbench.card.dl.projectFallback");
  const simpleType = options.simpleType ?? "";
  const allowedGroups = options.allowedGroups ?? ALL_GROUPS;
  const fullDubLanguages = new Set(
    options.assets
      .filter((asset) => asset.status === "ready" && asset.role === "full_dub_audio")
      .map((asset) => asset.language ?? ""),
  );
  const candidates = options.assets
    .filter((asset) => asset.status === "ready" && DOWNLOADABLE_TYPES.has(asset.type))
    .filter((asset) => {
      if (
        simpleType === "clone" &&
        asset.role === "dub_audio" &&
        fullDubLanguages.has(asset.language ?? "")
      ) {
        return false;
      }
      const role = asset.role;
      // 无/未知 role 保留为文件级条目，不能因其他资产有 role 而整体丢失。
      if (!isKnownRole(role)) return true;
      return roleVisible(simpleType, role);
    })
    .map((asset) => {
      const meta = metaFor(asset);
      const extension = assetExtension(asset, asset.role);
      return {
        asset,
        label: meta.label,
        previewLabel: meta.previewLabel,
        extension,
        group: roleGroup(asset, asset.role),
        leading: `${base}${meta.prefix}${meta.stem}`,
      };
    })
    // 原件与无/未知 role 的文件级条目不因能力分组丢项；已知其他角色仍按分组过滤。
    .filter(
      (item) =>
        !isKnownRole(item.asset.role) ||
        item.asset.role === "original" ||
        allowedGroups.includes(item.group),
    )
    .sort(compareItems);

  const used = new Set<string>();
  return candidates.map(({ leading, ...item }) => ({
    ...item,
    filename: uniqueFilename(leading, item.extension, used),
  }));
}
