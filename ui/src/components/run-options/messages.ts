import type { AreaMessages } from "@/i18n";

// Run 冻结选项的共用只读摘要（克隆、专属链路、全流程与项目卡片同口径）。
const messages: AreaMessages = {
  en: {
    "runOptions.title": "Recorded task options",
    "runOptions.frozenHint":
      "These options are fixed when a task is created; changing the current form does not affect tasks that already exist.",
    "runOptions.validateTimestamps": "Subtitle timestamp validation",
    "runOptions.adjustDuration": "Duration adjustment",
    "runOptions.on": "On",
    "runOptions.off": "Off",
    "runOptions.notRecorded": "Not recorded",
    "runOptions.adjustRecordedNotApplied": "Recorded on · not applied",
  },
  zhCN: {
    "runOptions.title": "任务选项记录",
    "runOptions.frozenHint": "这些选项在创建任务时固定；修改当前表单不会影响已创建的任务。",
    "runOptions.validateTimestamps": "字幕时间戳校验",
    "runOptions.adjustDuration": "时长动态调整",
    "runOptions.on": "已开启",
    "runOptions.off": "已关闭",
    "runOptions.notRecorded": "未记录",
    "runOptions.adjustRecordedNotApplied": "记录为开启但未生效",
  },
};

export default messages;
