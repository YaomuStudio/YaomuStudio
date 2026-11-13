// 宿主就绪快照的有限 phase/reason → 已注册文案键（不展示任意原始错误）。
import { t } from "@/i18n";

export function runtimePhaseLabel(phase: YaomuRuntimePhase): string {
  switch (phase) {
    case "preparing":
      return t("runtime.phase.preparing");
    case "starting":
      return t("runtime.phase.starting");
    case "ready":
      return t("runtime.phase.ready");
    case "restarting":
      return t("runtime.phase.restarting");
    case "failed":
      return t("runtime.phase.failed");
    case "stopping":
      return t("runtime.phase.stopping");
    case "stopped":
      return t("runtime.phase.stopped");
  }
  const exhaustive: never = phase;
  return exhaustive;
}

export function runtimeReasonLabel(reason: YaomuRuntimeReason): string {
  switch (reason) {
    case "startup_failed":
      return t("runtime.reason.startup_failed");
    case "core_exited":
      return t("runtime.reason.core_exited");
    case "cleanup_unconfirmed":
      return t("runtime.reason.cleanup_unconfirmed");
    case "handshake_failed":
      return t("runtime.reason.handshake_failed");
    case "protocol_mismatch":
      return t("runtime.reason.protocol_mismatch");
    case "workspace_mismatch":
      return t("runtime.reason.workspace_mismatch");
  }
  const exhaustive: never = reason;
  return exhaustive;
}
