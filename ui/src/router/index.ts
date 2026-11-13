import { createRouter, createWebHashHistory, type RouteRecordRaw } from "vue-router";
import Workbench from "@/pages/Workbench/Index.vue";
import Projects from "@/pages/Projects/Index.vue";
import ProjectDetail from "@/pages/Projects/ProjectDetail.vue";
import Pipeline from "@/pages/Pipeline/Index.vue";
import PipelineNew from "@/pages/Pipeline/New.vue";
import VoiceClone from "@/pages/VoiceClone/Index.vue";
import Erase from "@/pages/tools/Erase.vue";
import Separate from "@/pages/tools/Separate.vue";
import Translate from "@/pages/tools/Translate.vue";
import Extract from "@/pages/tools/Extract.vue";
import CustomPipeline from "@/pages/tools/CustomPipeline.vue";
import SettingsIndex from "@/pages/Settings/Index.vue";
import SettingsGeneral from "@/pages/Settings/General.vue";
import SettingsLanguage from "@/pages/Settings/Language.vue";
import SettingsAppearance from "@/pages/Settings/Appearance.vue";
import SettingsStorage from "@/pages/Settings/Storage.vue";
import SettingsDevices from "@/pages/Settings/Devices.vue";
import SettingsPrivacy from "@/pages/Settings/Privacy.vue";
import SettingsLocalApi from "@/pages/Settings/LocalApi.vue";
import SettingsNotifications from "@/pages/Settings/Notifications.vue";
import SettingsAbout from "@/pages/Settings/About.vue";
import Models from "@/pages/Models/Index.vue";
import Onboarding from "@/pages/Onboarding/Index.vue";
import ManualCorrection from "@/pages/ManualCorrection/Index.vue";

// 静态导入：Electron file:// 下避免动态 chunk 加载问题。
const routes: RouteRecordRaw[] = [
  { path: "/", name: "workbench", component: Workbench },
  { path: "/projects", name: "projects", component: Projects },
  { path: "/projects/:id", name: "project-detail", component: ProjectDetail },
  {
    path: "/projects/:id/pipeline",
    name: "project-pipeline",
    component: Pipeline,
    meta: { accessKey: "pipeline" },
  },
  {
    path: "/projects/:id/correction",
    redirect: (to) => ({ path: `/manual-correction/${String(to.params.id ?? "")}` }),
  },
  { path: "/manual-correction/:id", name: "manual-correction", component: ManualCorrection },
  { path: "/tasks", redirect: "/projects" },
  // accessKey 由 main.ts 的路由守卫按发布准入隐藏/放行；无该字段的路由始终可访问。
  {
    path: "/pipeline/new",
    name: "pipeline-new",
    component: PipelineNew,
    meta: { accessKey: "pipeline" },
  },
  {
    path: "/pipeline",
    redirect: (to) => {
      const project = typeof to.query.project === "string" ? to.query.project : "";
      return project
        ? { path: `/projects/${project}/pipeline`, query: to.query }
        : { path: "/pipeline/new" };
    },
  },
  {
    path: "/voice-clone",
    name: "voice-clone",
    component: VoiceClone,
    meta: { accessKey: "voice-clone" },
  },
  { path: "/tools/erase", name: "tool-erase", component: Erase, meta: { accessKey: "tool-erase" } },
  {
    path: "/tools/separate",
    name: "tool-separate",
    component: Separate,
    meta: { accessKey: "tool-separate" },
  },
  {
    path: "/tools/translate",
    name: "tool-translate",
    component: Translate,
    meta: { accessKey: "tool-translate" },
  },
  {
    path: "/tools/extract",
    name: "tool-extract",
    component: Extract,
    meta: { accessKey: "tool-extract" },
  },
  {
    path: "/tools/custom",
    name: "tool-custom",
    component: CustomPipeline,
    meta: { accessKey: "tool-custom" },
  },
  {
    path: "/settings",
    component: SettingsIndex,
    children: [
      { path: "", redirect: "/settings/general" },
      { path: "general", name: "settings-general", component: SettingsGeneral },
      { path: "language", name: "settings-language", component: SettingsLanguage },
      { path: "appearance", name: "settings-appearance", component: SettingsAppearance },
      { path: "storage", name: "settings-storage", component: SettingsStorage },
      { path: "devices", name: "settings-devices", component: SettingsDevices },
      { path: "privacy", name: "settings-privacy", component: SettingsPrivacy },
      { path: "local-api", name: "settings-local-api", component: SettingsLocalApi },
      { path: "notifications", name: "settings-notifications", component: SettingsNotifications },
      { path: "about", name: "settings-about", component: SettingsAbout },
      { path: "models", name: "settings-models", component: Models },
    ],
  },
  { path: "/onboarding", name: "onboarding", component: Onboarding },
  { path: "/:pathMatch(.*)*", redirect: "/" },
];

export const router = createRouter({
  history: createWebHashHistory(),
  routes,
});
