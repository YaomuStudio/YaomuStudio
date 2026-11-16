import type { AreaMessages } from "@/i18n";

// 通用文案键（tools.common.*）由 pages/tools/messages.ts 一并注册，两者同属工具/生产页面区域。
const messages: AreaMessages = {
  en: {
    "voiceClone.title": "High-emotion clone dubbing",
    "voiceClone.intro":
      "Upload a clean vocal track, original and translated SRTs, then choose target languages. No microphone capture.",

    "voiceClone.step.source": "1 · Clean vocal",
    "voiceClone.step.subtitles": "2 · Subtitles & languages",
    "voiceClone.step.options": "3 · Execution options",

    "voiceClone.source.label": "Clean vocal track",
    "voiceClone.source.upload": "Upload clean vocal track",
    "voiceClone.source.dropHint": "Click to choose a vocal file without music or effects.",
    "voiceClone.source.hint": "MP3 / WAV / AAC / FLAC / M4A, up to 120 minutes. A lossless WAV dry vocal is recommended.",
    "voiceClone.source.err": "Upload a clean vocal track.",
    "voiceClone.source.warn": "Background music, ambience, reverb, or overlapping speakers interfere with voice cloning and may add noise or make the result sound unlike the speaker.",
    "voiceClone.source.videoHint":
      "Audio only, and it must be a clean vocal. For video sources, extract the vocal track with Audio Separation first and make sure no music, ambience, or overlapping speakers remain.",

    "voiceClone.sub.original": "Original subtitles",
    "voiceClone.sub.originalUpload": "Upload original subtitles (SRT)",
    "voiceClone.sub.originalHint": "SRT subtitles aligned with the clean vocal timeline.",
    "voiceClone.sub.originalErr": "Original SRT subtitles are required.",
    "voiceClone.sub.originalDetected": "Detected language: {language}",
    "voiceClone.sub.originalDetectPending": "Subtitle language will be detected automatically.",
    "voiceClone.sub.sourceLanguage": "Original subtitle language",
    "voiceClone.sub.sourceLanguageHint": "Used for the imported transcription track.",

    "voiceClone.sub.target": "Dubbing target languages",
    "voiceClone.sub.targetHint": "Select one or more languages.",
    "voiceClone.sub.targetErr": "Select at least one target language.",

    "voiceClone.sub.translated": "Translated subtitles",
    "voiceClone.sub.translatedUpload": "Batch upload translated subtitles (SRT)",
    "voiceClone.sub.translatedHint":
      "Batch upload is supported; languages are detected from subtitle content. One file per language.",
    "voiceClone.sub.translatedErr": "Translated subtitles are required.",
    "voiceClone.sub.language": "Target language",
    "voiceClone.sub.languagePlaceholder": "Choose language",
    "voiceClone.sub.languageErr":
      "Each translated file needs a unique target language; add more target languages or remove extra files.",
    "voiceClone.sub.languageCount":
      "Target languages and translated subtitles must match one-to-one ({targets} languages, {files} files).",
    "voiceClone.sub.languageSource":
      "Translated subtitles cannot use the source language ({language}).",
    "voiceClone.sub.translatedAuto": "Detected from file name",
    "voiceClone.sub.countHint": "Keep the number of original and translated subtitle entries as consistent as possible.",

    "voiceClone.opt.validate": "Validate subtitle timestamps",
    "voiceClone.opt.validateHint":
      "Checks original and translated subtitle start/end times against the selected media duration for the step 07 clone; media without a reliable duration is rejected. Subtitle format and ordering checks always run.",
    "voiceClone.opt.adjust": "Dynamic duration adjustment",
    "voiceClone.opt.adjustHint":
      "Not supported in this version: dubbing duration is not stretched or shrunk, so this option is disabled.",

    "voiceClone.result.desc": "Clone progress and the recommended audio for each line appear here.",
    "voiceClone.result.idle": "Not started",
    "voiceClone.result.running": "Cloning",
    "voiceClone.result.emptyTitle": "Created tasks appear here",
    "voiceClone.result.emptyDesc":
      "Upload a clean vocal track and subtitles, choose languages, then click Create.",
    "voiceClone.result.missing": "Clone finished but translated tracks or audio candidates were missing.",
    "voiceClone.result.noCandidate": "No audio candidate for this line.",
    "voiceClone.result.candidate": "Recommended · {model}",
    "voiceClone.result.lineCount": "{count} lines",
    "voiceClone.result.save": "Save audio",
    "voiceClone.result.saved": "Saved to {path}",
    "voiceClone.result.saveFailed": "Saving the audio failed.",
    "voiceClone.pathUnavailable": "A selected file path is unavailable. Pick the file again.",
    "voiceClone.importFailed": "Importing the source audio or subtitles failed.",

    "voiceClone.summary.source": "Clean vocal track",
    "voiceClone.summary.original": "Original subtitles",
    "voiceClone.summary.languages": "Target languages",
    "voiceClone.summary.translated": "Translated subtitles",
    "voiceClone.summary.validate": "Timestamp validation",
    "voiceClone.summary.adjust": "Duration adjustment",
  },
  zhCN: {
    "voiceClone.title": "高情感克隆配音",
    "voiceClone.intro":
      "上传纯净人声轨、原文字幕、译文字幕与目标语言；克隆按模型 1:1 还原音色，不涉及麦克风录制。",

    "voiceClone.step.source": "1 · 纯净人声",
    "voiceClone.step.subtitles": "2 · 字幕与语言",
    "voiceClone.step.options": "3 · 执行选项",

    "voiceClone.source.label": "纯净人声轨",
    "voiceClone.source.upload": "上传纯净人声轨",
    "voiceClone.source.dropHint": "点击选择无背景音乐、无音效的人声文件。",
    "voiceClone.source.hint": "支持 MP3 / WAV / AAC / FLAC / M4A，时长 ≤120 分钟；建议使用 WAV 无损干声。",
    "voiceClone.source.err": "请上传纯净人声轨。",
    "voiceClone.source.warn": "背景音乐、环境音、混响或多人对白会干扰音色还原，可能导致杂音或不像本人。",
    "voiceClone.source.videoHint": "这里只接收音频，且必须是纯净人声：视频请先在「音频分离」中提取人声轨，并确认没有背景音乐、环境音或多人对白后再上传。",

    "voiceClone.sub.original": "原文字幕",
    "voiceClone.sub.originalUpload": "上传原文字幕（SRT）",
    "voiceClone.sub.originalHint": "SRT 字幕，须与纯净人声轨时间轴一致。",
    "voiceClone.sub.originalErr": "原文字幕 SRT 为必传项。",
    "voiceClone.sub.originalDetected": "已自动识别：{language}",
    "voiceClone.sub.originalDetectPending": "将自动识别字幕语言。",
    "voiceClone.sub.sourceLanguage": "原文字幕语言",
    "voiceClone.sub.sourceLanguageHint": "用于创建导入的转写轨。",

    "voiceClone.sub.target": "配音目标语言",
    "voiceClone.sub.targetHint": "可多选。",
    "voiceClone.sub.targetErr": "请至少选择一个目标语言。",

    "voiceClone.sub.translated": "译文字幕",
    "voiceClone.sub.translatedUpload": "批量上传译文字幕（SRT）",
    "voiceClone.sub.translatedHint": "支持批量上传；按字幕内容自动识别语言，每种语言一份文件。",
    "voiceClone.sub.translatedErr": "译文字幕为必传项。",
    "voiceClone.sub.language": "目标语言",
    "voiceClone.sub.languagePlaceholder": "选择语言",
    "voiceClone.sub.languageErr": "每个译文字幕文件需要唯一的目标语言；请增加目标语言或移除多余文件。",
    "voiceClone.sub.languageCount": "目标语言与译文字幕需要一一对应（{targets} 种语言，{files} 份文件）。",
    "voiceClone.sub.languageSource": "译文字幕不能使用原文语种（{language}）。",
    "voiceClone.sub.translatedAuto": "按文件名识别",
    "voiceClone.sub.countHint": "原文与译文字幕条数尽量一致。",

    "voiceClone.opt.validate": "字幕时间戳校验",
    "voiceClone.opt.validateHint":
      "按所选媒体时长检查原文与译文字幕的起止时间（07 克隆步骤）；媒体缺少可靠时长时会拒绝。字幕格式与顺序检查始终执行。",
    "voiceClone.opt.adjust": "时长动态调整",
    "voiceClone.opt.adjustHint": "当前版本暂不支持时长调整：不会伸缩配音时长，因此该选项不可用。",

    "voiceClone.result.desc": "克隆进度与每句推荐音频在此显示。",
    "voiceClone.result.idle": "未开始",
    "voiceClone.result.running": "克隆中",
    "voiceClone.result.emptyTitle": "创建后在此显示进度/结果",
    "voiceClone.result.emptyDesc": "上传纯净人声轨与字幕、选择目标语言后，点击「创建」。",
    "voiceClone.result.missing": "克隆完成，但缺少译文轨或音频候选。",
    "voiceClone.result.noCandidate": "该句没有音频候选。",
    "voiceClone.result.candidate": "推荐 · {model}",
    "voiceClone.result.lineCount": "{count} 句",
    "voiceClone.result.save": "保存音频",
    "voiceClone.result.saved": "已保存到 {path}",
    "voiceClone.result.saveFailed": "保存音频失败。",
    "voiceClone.pathUnavailable": "所选文件路径不可用，请重新选择文件。",
    "voiceClone.importFailed": "声源或字幕导入失败。",

    "voiceClone.summary.source": "纯净人声轨",
    "voiceClone.summary.original": "原文字幕",
    "voiceClone.summary.languages": "目标语言",
    "voiceClone.summary.translated": "译文字幕",
    "voiceClone.summary.validate": "时间戳校验",
    "voiceClone.summary.adjust": "时长调整",
  },
};

export default messages;
