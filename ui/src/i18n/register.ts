// 汇总注册全部文案（壳层 + 各页面区域）。
// 页面开发只改本区域 messages.ts，不改本文件。
import { registerMessages } from "./index";
import base from "./base";
import workbench from "@/pages/Workbench/messages";
import projects from "@/pages/Projects/messages";
import projectCards from "@/pages/Projects/cardMessages";
import taskCenter from "@/pages/TaskCenter/messages";
import pipeline from "@/pages/Pipeline/messages";
import voiceClone from "@/pages/VoiceClone/messages";
import tools from "@/pages/tools/messages";
import runOptions from "@/components/run-options/messages";
import settings from "@/pages/Settings/messages";
import models from "@/pages/Models/messages";
import onboarding from "@/pages/Onboarding/messages";
import manualCorrection from "@/pages/ManualCorrection/messages";
import runtime from "@/components/runtime/messages";

const areas = [
  base,
  workbench,
  projects,
  projectCards,
  taskCenter,
  pipeline,
  voiceClone,
  tools,
  runOptions,
  settings,
  models,
  onboarding,
  manualCorrection,
  runtime,
];

for (const area of areas) registerMessages(area);
