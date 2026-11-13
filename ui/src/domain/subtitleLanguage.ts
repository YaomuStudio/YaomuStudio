import { TARGET_LANGUAGES } from "./languages";

/**
 * 文件名里的语言码/名称只接受常见分隔符包围的独立 token。
 * 不能用“任意非字母数字”做边界：`wsy泰语cs_原文字幕.srt` 里的 `cs`
 * 会被误判成捷克语。这里只认 ASCII 分隔符，CJK 字符不算边界。
 */
const NAME_SEPARATOR = String.raw`[._\-\s()\[\]{}（）【】]`;

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** 从文件名识别语言码；语言码/名称必须作为独立 token 出现。 */
export function detectLanguageFromName(fileName: string): string | undefined {
  const lower = fileName.toLowerCase();
  const byCodeLength = [...TARGET_LANGUAGES].sort((a, b) => b.code.length - a.code.length);
  for (const language of byCodeLength) {
    const code = escapeRegExp(language.code.toLowerCase());
    const token = new RegExp(`(?:^|${NAME_SEPARATOR})${code}(?:$|${NAME_SEPARATOR})`, "u");
    if (token.test(lower)) return language.code;
  }
  const byNameLength = [...TARGET_LANGUAGES].sort((a, b) => b.name.length - a.name.length);
  for (const language of byNameLength) {
    const names = [language.name, language.nativeName ?? ""].filter((name) => name.length >= 2);
    if (names.some((name) => lower.includes(name.toLowerCase()))) return language.code;
  }
  return undefined;
}

/** 字幕内容里的强脚本信号；拉丁文字无法只靠字符集区分语言，留给文件名判断。 */
interface ScriptSignal {
  code?: string;
  hasLatin: boolean;
}

function detectScriptLanguage(text: string): ScriptSignal {
  if (/[\u3040-\u30ff]/u.test(text)) return { code: "ja", hasLatin: false };
  if (/[\uac00-\ud7af]/u.test(text)) return { code: "ko", hasLatin: false };
  if (/[\u0600-\u06ff]/u.test(text)) return { code: "ar", hasLatin: false };
  if (/[\u0590-\u05ff]/u.test(text)) return { code: "he", hasLatin: false };
  if (/[\u0900-\u097f]/u.test(text)) return { code: "hi", hasLatin: false };
  if (/[\u0e00-\u0e7f]/u.test(text)) return { code: "th", hasLatin: false };
  if (/[\u0400-\u04ff]/u.test(text)) return { code: "ru", hasLatin: false };
  if (/[\u4e00-\u9fff]/u.test(text)) return { code: "zh-CN", hasLatin: false };
  return { hasLatin: /[A-Za-z\u00c0-\u024f]/u.test(text) };
}

/** 去掉时间轴与标签，避免把 `-->` 等符号计入判断。 */
function stripSubtitleSyntax(text: string): string {
  return text
    .replace(/\d{2}:\d{2}:\d{2}[,.]\d{3}\s*-->\s*\d{2}:\d{2}:\d{2}[,.]\d{3}/g, " ")
    .replace(/<[^>]*>/g, " ")
    .slice(0, 20000);
}

async function readSubtitleSample(file: File): Promise<string> {
  try {
    return stripSubtitleSyntax(await file.text());
  } catch {
    return "";
  }
}

/** 仅按内容做脚本级判断；无法区分拉丁语种时返回 undefined。 */
export async function detectSubtitleLanguage(file: File): Promise<string | undefined> {
  const sample = await readSubtitleSample(file);
  return sample ? detectScriptLanguage(sample).code : undefined;
}

/**
 * 文件语言识别优先级（与网页端口径一致）：
 * 1. 内容出现明确非拉丁脚本（中文/泰文/日文/韩文等）时以内容为准；
 * 2. 否则使用文件名里的独立语言码/名称（如 `movie.th.srt`）；
 * 3. 纯拉丁内容按英文兜底。
 */
export async function detectFileLanguage(file: File): Promise<string | undefined> {
  const sample = await readSubtitleSample(file);
  const script = detectScriptLanguage(sample);
  if (script.code) return script.code;
  const byName = detectLanguageFromName(file.name);
  if (byName) return byName;
  return script.hasLatin ? "en" : undefined;
}
