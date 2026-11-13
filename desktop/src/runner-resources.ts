/**
 * 随包 Python Runner 资源校验（唯一消费入口）。
 *
 * 权威：contracts/packaged-runner-resources.md 与 schema。
 * - 只读取应用自有资源目录：`<coreDir>/runner_src` 与 `<coreDir>/runner-manifest.json`。
 * - 清单读取有上限（默认 1 MiB），逐文件流式 SHA-256，不整树读入内存。
 * - 路径、文件集合、大小、摘要、版本格式与入口文件都必须一致；
 *   链接/特殊文件、路径逃逸、大小写折叠重复、未列出的额外文件都判失败。
 * - 部署树中的 `__pycache__`/`.pyc`/`.pyo`（不分大小写）必须拒绝：
 *   `-B` 只禁止写入，仍会读取已有缓存；源码哈希一致不能证明执行来自源码。
 * - 这是本地装配一致性校验，不是签名或来源认证；解释器/镜像/OS 签名另行门槛。
 * - 校验失败返回固定 reason，不抛异常；取消（AbortSignal）按 AbortError 中止，
 *   由调用方停止后续准备，不得从过期准备继续启动 Runner。
 */
import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import { lstat, open, readdir } from "node:fs/promises";
import path from "node:path";
import { abortError } from "./bootstrap";

/** 交给纯 WSL argv 投影的窄内部描述；不来自 Renderer，也不进入业务状态。 */
export interface BundledRunnerDescriptor {
  /** 应用自有 Runner 源码目录的原生绝对路径（未映射到 WSL 路径）。 */
  sourceDir: string;
  runnerVersion: string;
  /** 清单文件自身的 SHA-256（诊断身份，不是自引用字段）。 */
  manifestDigest: string;
}

export type RunnerResourceFailureReason =
  | "manifest-missing"
  | "manifest-unreadable"
  | "manifest-too-large"
  | "manifest-invalid"
  | "entry-missing"
  | "path-invalid"
  | "path-duplicate"
  | "file-missing"
  | "file-extra"
  | "file-mismatch"
  | "bytecode-cache"
  | "special-file"
  | "path-escape"
  | "limits-exceeded";

export type RunnerResourceValidation =
  | { ok: true; descriptor: BundledRunnerDescriptor }
  | { ok: false; reason: RunnerResourceFailureReason; detail: string };

export interface ValidateBundledRunnerResourcesOptions {
  /** 拥有 `runner_src` 与 `runner-manifest.json` 的资源目录（打包态为 resourcesPath/core）。 */
  coreDir: string;
  signal?: AbortSignal;
  /** 清单文件读取上限；默认 1 MiB。 */
  maxManifestBytes?: number;
  /** 清单文件条目上限；默认 4096（与 schema 一致）。 */
  maxFiles?: number;
}

const DEFAULT_MAX_MANIFEST_BYTES = 1024 * 1024;
const DEFAULT_MAX_FILES = 4096;
const HASH_CHUNK_BYTES = 1024 * 1024;
// 与 contracts/packaged-runner-resources.schema.json 保持一致。
const RUNNER_VERSION_PATTERN = /^[A-Za-z0-9][A-Za-z0-9.+_-]*$/;
const FILE_PATH_PATTERN =
  /^yaomustudio_runner\/(?:[A-Za-z0-9_][A-Za-z0-9_.-]*\/)*[A-Za-z0-9_][A-Za-z0-9_.-]*$/;
const SHA256_PATTERN = /^[a-f0-9]{64}$/;
const REQUIRED_PYTHON = ">=3.11";
const ENTRY_FILES = [
  "yaomustudio_runner/__init__.py",
  "yaomustudio_runner/__main__.py",
  "yaomustudio_runner/main.py",
] as const;
const MANIFEST_KEYS = new Set([
  "kind",
  "schema_version",
  "runner_version",
  "requires_python",
  "files",
]);
const FILE_KEYS = new Set(["path", "size", "sha256"]);

function throwIfAborted(signal?: AbortSignal): void {
  if (signal?.aborted) throw abortError("Runner 资源校验已取消");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function failure(reason: RunnerResourceFailureReason, detail: string): RunnerResourceValidation {
  return { ok: false, reason, detail };
}

function isInside(root: string, candidate: string): boolean {
  const relative = path.relative(root, candidate);
  return relative !== "" && !relative.startsWith("..") && !path.isAbsolute(relative);
}

/**
 * Python 字节码缓存必须被拒绝，不是忽略项：
 * `-B` 只禁止写入，仍允许读取已存在的 `__pycache__`/`.pyc` 缓存，
 * 即使源码字节和清单一致，执行也可能来自未验证的缓存代码。
 * Windows 文件系统大小写不敏感，按小写比较。
 */
function isBytecodeCache(relativePath: string): boolean {
  const lower = relativePath.toLowerCase();
  return (
    lower.split("/").includes("__pycache__") ||
    lower.endsWith(".pyc") ||
    lower.endsWith(".pyo")
  );
}

interface DiskFile {
  size: number;
}

type Enumeration =
  | { ok: true; files: Map<string, DiskFile> }
  | { ok: false; reason: RunnerResourceFailureReason; detail: string };

/**
 * 枚举自有源码树：只接受普通目录/文件，忽略 Python 字节码缓存。
 * 链接、特殊文件、逃逸路径与包外条目都判失败；不跟随链接。
 */
async function enumerateSourceTree(
  sourceDir: string,
  signal: AbortSignal | undefined,
  maxFiles: number,
): Promise<Enumeration> {
  const root = path.resolve(sourceDir);
  let rootStat;
  try {
    rootStat = await lstat(root);
  } catch {
    return { ok: false, reason: "file-missing", detail: "runner_src" };
  }
  if (rootStat.isSymbolicLink() || !rootStat.isDirectory()) {
    return { ok: false, reason: "special-file", detail: "runner_src" };
  }

  const files = new Map<string, DiskFile>();
  const pending: string[] = [""];
  while (pending.length > 0) {
    throwIfAborted(signal);
    const relativeDir = pending.pop() as string;
    const absoluteDir = relativeDir ? path.join(root, ...relativeDir.split("/")) : root;
    if (relativeDir && !isInside(root, absoluteDir)) {
      return { ok: false, reason: "path-escape", detail: relativeDir };
    }
    let entries;
    try {
      entries = await readdir(absoluteDir, { withFileTypes: true });
    } catch {
      return { ok: false, reason: "file-missing", detail: relativeDir || "runner_src" };
    }
    for (const entry of entries) {
      throwIfAborted(signal);
      const relative = relativeDir ? `${relativeDir}/${entry.name}` : entry.name;
      // 部署树中的字节码缓存不可被当成可忽略文件：拒绝而不是跳过。
      if (isBytecodeCache(relative)) {
        return { ok: false, reason: "bytecode-cache", detail: relative };
      }
      if (entry.isSymbolicLink()) {
        return { ok: false, reason: "special-file", detail: relative };
      }
      const absolute = path.join(absoluteDir, entry.name);
      if (!isInside(root, absolute)) {
        return { ok: false, reason: "path-escape", detail: relative };
      }
      if (entry.isDirectory()) {
        if (relative !== "yaomustudio_runner" && !relative.startsWith("yaomustudio_runner/")) {
          return { ok: false, reason: "path-invalid", detail: relative };
        }
        pending.push(relative);
        continue;
      }
      if (!entry.isFile()) {
        return { ok: false, reason: "special-file", detail: relative };
      }
      if (!FILE_PATH_PATTERN.test(relative)) {
        return { ok: false, reason: "path-invalid", detail: relative };
      }
      let stat;
      try {
        stat = await lstat(absolute);
      } catch {
        // 枚举后消失/不可读是普通资源不可用，不能让校验 promise 拒绝并阻断 Core。
        return { ok: false, reason: "file-missing", detail: relative };
      }
      if (stat.isSymbolicLink() || !stat.isFile()) {
        return { ok: false, reason: "special-file", detail: relative };
      }
      files.set(relative, { size: stat.size });
      if (files.size > maxFiles) {
        return { ok: false, reason: "limits-exceeded", detail: "files" };
      }
    }
  }
  return { ok: true, files };
}

async function sha256File(absolutePath: string, signal?: AbortSignal): Promise<string> {
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(absolutePath, {
    highWaterMark: HASH_CHUNK_BYTES,
  })) {
    throwIfAborted(signal);
    hash.update(chunk as Buffer);
  }
  return hash.digest("hex");
}

/**
 * 读取清单文件：先 stat 限制大小，再读 size+1 字节检测读取期间增长；
 * 返回原始字节的 SHA-256 作为诊断身份。
 */
async function readManifest(
  manifestPath: string,
  maxBytes: number,
  signal?: AbortSignal,
): Promise<
  | { ok: true; text: string; digest: string }
  | { ok: false; reason: RunnerResourceFailureReason; detail: string }
> {
  throwIfAborted(signal);
  let stat;
  try {
    stat = await lstat(manifestPath);
  } catch (error) {
    return {
      ok: false,
      reason: (error as NodeJS.ErrnoException).code === "ENOENT" ? "manifest-missing" : "manifest-unreadable",
      detail: "runner-manifest.json",
    };
  }
  if (stat.isSymbolicLink() || !stat.isFile()) {
    return { ok: false, reason: "special-file", detail: "runner-manifest.json" };
  }
  if (stat.size <= 0) {
    return { ok: false, reason: "manifest-invalid", detail: "empty" };
  }
  if (stat.size > maxBytes) {
    return { ok: false, reason: "manifest-too-large", detail: "runner-manifest.json" };
  }

  let handle;
  try {
    handle = await open(manifestPath, "r");
    // 多读 1 字节：读取期间文件增长同样按超限处理，不把截断内容当完整清单。
    const buffer = Buffer.alloc(stat.size + 1);
    const { bytesRead } = await handle.read(buffer, 0, stat.size + 1, 0);
    if (bytesRead > stat.size) {
      return { ok: false, reason: "manifest-too-large", detail: "runner-manifest.json" };
    }
    if (bytesRead !== stat.size) {
      return { ok: false, reason: "manifest-unreadable", detail: "runner-manifest.json" };
    }
    const bytes = buffer.subarray(0, bytesRead);
    throwIfAborted(signal);
    return {
      ok: true,
      text: bytes.toString("utf8"),
      digest: createHash("sha256").update(bytes).digest("hex"),
    };
  } catch (error) {
    // 取消必须向上传播，不能被当作清单读失败继续校验。
    if (error instanceof Error && error.name === "AbortError") throw error;
    return { ok: false, reason: "manifest-unreadable", detail: "runner-manifest.json" };
  } finally {
    await handle?.close().catch(() => undefined);
  }
}

interface ManifestEntry {
  path: string;
  size: number;
  sha256: string;
}

interface ParsedManifest {
  runnerVersion: string;
  files: ManifestEntry[];
}

function parseManifest(text: string): ParsedManifest | RunnerResourceValidation {
  let value: unknown;
  try {
    value = JSON.parse(text) as unknown;
  } catch {
    return failure("manifest-invalid", "json");
  }
  if (!isRecord(value)) return failure("manifest-invalid", "root");
  for (const key of Object.keys(value)) {
    if (!MANIFEST_KEYS.has(key)) return failure("manifest-invalid", "unknown-field");
  }
  if (value.kind !== "yaomu_runner_resources") return failure("manifest-invalid", "kind");
  if (value.schema_version !== 1) return failure("manifest-invalid", "schema-version");
  const runnerVersion = value.runner_version;
  if (
    typeof runnerVersion !== "string" ||
    runnerVersion.length === 0 ||
    runnerVersion.length > 64 ||
    !RUNNER_VERSION_PATTERN.test(runnerVersion)
  ) {
    return failure("manifest-invalid", "runner-version");
  }
  if (value.requires_python !== REQUIRED_PYTHON) {
    return failure("manifest-invalid", "requires-python");
  }
  if (!Array.isArray(value.files)) return failure("manifest-invalid", "files");
  if (value.files.length < 3) return failure("manifest-invalid", "files-too-few");
  if (value.files.length > DEFAULT_MAX_FILES) {
    return failure("manifest-invalid", "files-too-many");
  }

  const files: ManifestEntry[] = [];
  const seen = new Set<string>();
  const seenFolded = new Set<string>();
  for (const raw of value.files) {
    if (!isRecord(raw)) return failure("manifest-invalid", "file-entry");
    for (const key of Object.keys(raw)) {
      if (!FILE_KEYS.has(key)) return failure("manifest-invalid", "file-unknown-field");
    }
    const filePath = raw.path;
    if (
      typeof filePath !== "string" ||
      filePath.length === 0 ||
      filePath.length > 512 ||
      !FILE_PATH_PATTERN.test(filePath)
    ) {
      return failure("manifest-invalid", "file-path");
    }
    const size = raw.size;
    if (typeof size !== "number" || !Number.isSafeInteger(size) || size < 0) {
      return failure("manifest-invalid", "file-size");
    }
    const sha256 = raw.sha256;
    if (typeof sha256 !== "string" || !SHA256_PATTERN.test(sha256)) {
      return failure("manifest-invalid", "file-sha256");
    }
    if (seen.has(filePath)) return failure("path-duplicate", filePath);
    seen.add(filePath);
    // Windows 文件系统大小写不敏感：折叠后重复路径不可信。
    const folded = filePath.toLowerCase();
    if (seenFolded.has(folded)) return failure("path-duplicate", filePath);
    seenFolded.add(folded);
    const previous = files[files.length - 1];
    if (previous && !(previous.path < filePath)) {
      return failure("manifest-invalid", "file-order");
    }
    files.push({ path: filePath, size, sha256 });
  }

  for (const entry of ENTRY_FILES) {
    if (!seen.has(entry)) return failure("entry-missing", entry);
  }
  return { runnerVersion, files };
}

/**
 * 异步校验随包 Runner 资源。
 * 资源问题返回固定 reason（调用方可保持 Core 编辑/诊断可用）；取消抛 AbortError。
 */
export async function validateBundledRunnerResources(
  options: ValidateBundledRunnerResourcesOptions,
): Promise<RunnerResourceValidation> {
  const signal = options.signal;
  throwIfAborted(signal);
  const maxManifestBytes = options.maxManifestBytes ?? DEFAULT_MAX_MANIFEST_BYTES;
  const maxFiles = options.maxFiles ?? DEFAULT_MAX_FILES;
  const coreDir = path.resolve(options.coreDir);
  const sourceDir = path.join(coreDir, "runner_src");
  const manifestPath = path.join(coreDir, "runner-manifest.json");

  const manifest = await readManifest(manifestPath, maxManifestBytes, signal);
  if (!manifest.ok) return failure(manifest.reason, manifest.detail);
  const parsed = parseManifest(manifest.text);
  if ("ok" in parsed) return parsed;

  const enumeration = await enumerateSourceTree(sourceDir, signal, maxFiles);
  if (!enumeration.ok) return failure(enumeration.reason, enumeration.detail);
  const diskFiles = enumeration.files;

  const manifestPaths = new Set(parsed.files.map((entry) => entry.path));
  for (const entry of parsed.files) {
    if (!diskFiles.has(entry.path)) return failure("file-missing", entry.path);
  }
  for (const diskPath of diskFiles.keys()) {
    if (!manifestPaths.has(diskPath)) return failure("file-extra", diskPath);
  }

  for (const entry of parsed.files) {
    throwIfAborted(signal);
    const disk = diskFiles.get(entry.path);
    if (!disk || disk.size !== entry.size) {
      return failure("file-mismatch", entry.path);
    }
    let digest;
    try {
      digest = await sha256File(path.join(sourceDir, ...entry.path.split("/")), signal);
    } catch (error) {
      if (error instanceof Error && error.name === "AbortError") throw error;
      return failure("file-missing", entry.path);
    }
    if (digest !== entry.sha256) return failure("file-mismatch", entry.path);
  }

  return {
    ok: true,
    descriptor: {
      sourceDir,
      runnerVersion: parsed.runnerVersion,
      manifestDigest: manifest.digest,
    },
  };
}
