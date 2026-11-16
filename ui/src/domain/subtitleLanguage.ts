/**
 * 字幕语种识别只看文件内容，不看文件名/标题。
 *
 * 识别基于整份字幕的字符脚本统计：中文/日文/韩文/泰文/阿拉伯文等脚本
 * 各自计数后取占比最高者；纯拉丁内容无法仅凭字符集区分语种，按英文兜底。
 * 文件名里的 `cs`、`泰语` 等字样不参与判断，避免“wsy泰语cs_原文字幕.srt”
 * 这类素材名把中文字幕误判成泰语或捷克语。
 */

interface ScriptSignal {
  code?: string;
  hasLatin: boolean;
}

const SCRIPTS = {
  kana: { pattern: /[\u3040-\u30ff]/gu, code: "ja" },
  hangul: { pattern: /[\uac00-\ud7af]/gu, code: "ko" },
  arabic: { pattern: /[\u0600-\u06ff]/gu, code: "ar" },
  hebrew: { pattern: /[\u0590-\u05ff]/gu, code: "he" },
  devanagari: { pattern: /[\u0900-\u097f]/gu, code: "hi" },
  thai: { pattern: /[\u0e00-\u0e7f]/gu, code: "th" },
  cyrillic: { pattern: /[\u0400-\u04ff]/gu, code: "ru" },
  han: { pattern: /[\u4e00-\u9fff]/gu, code: "zh-CN" },
  latin: { pattern: /[A-Za-z\u00c0-\u024f]/gu, code: "en" },
} as const;

function detectScriptLanguage(text: string): ScriptSignal {
  const counts: Record<keyof typeof SCRIPTS, number> = {
    kana: 0,
    hangul: 0,
    arabic: 0,
    hebrew: 0,
    devanagari: 0,
    thai: 0,
    cyrillic: 0,
    han: 0,
    latin: 0,
  };
  for (const key of Object.keys(SCRIPTS) as (keyof typeof SCRIPTS)[]) {
    counts[key] = [...text.matchAll(SCRIPTS[key].pattern)].length;
  }
  let winner: keyof typeof SCRIPTS | undefined;
  for (const key of Object.keys(SCRIPTS) as (keyof typeof SCRIPTS)[]) {
    if (counts[key] === 0) continue;
    if (!winner || counts[key] > counts[winner]) winner = key;
  }
  if (winner && winner !== "latin") return { code: SCRIPTS[winner].code, hasLatin: false };
  return { hasLatin: counts.latin > 0 };
}

/** 去掉时间轴与标签，避免把 `-->` 等符号计入判断；不截断内容。 */
function stripSubtitleSyntax(text: string): string {
  return text
    .replace(/\d{2}:\d{2}:\d{2}[,.]\d{3}\s*-->\s*\d{2}:\d{2}:\d{2}[,.]\d{3}/g, " ")
    .replace(/<[^>]*>/g, " ");
}

async function readSubtitleContent(file: File): Promise<string> {
  try {
    return stripSubtitleSyntax(await file.text());
  } catch {
    return "";
  }
}

/** 仅按整份字幕内容做脚本级判断；无法区分拉丁语种时返回 undefined。 */
export async function detectSubtitleLanguage(file: File): Promise<string | undefined> {
  const content = await readSubtitleContent(file);
  return content ? detectScriptLanguage(content).code : undefined;
}

/**
 * 文件语言识别只使用内容：
 * 1. 全量统计字符脚本，取占比最高的非拉丁脚本；
 * 2. 纯拉丁内容按英文兜底。
 * 文件名不参与识别。
 */
export async function detectFileLanguage(file: File): Promise<string | undefined> {
  const content = await readSubtitleContent(file);
  if (!content) return undefined;
  const script = detectScriptLanguage(content);
  if (script.code) return script.code;
  return script.hasLatin ? "en" : undefined;
}
