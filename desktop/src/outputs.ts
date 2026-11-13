/** 任务产出的默认目录解析；纯函数，便于在不加载 Electron 的测试中验证。 */
import path from "node:path";

export interface ProgramPaths {
  /** Electron 应用是否已打包。 */
  packaged: boolean;
  /** app.getAppPath()：开发态为 desktop/，打包态为 app.asar。 */
  appPath: string;
  /** process.execPath：打包态为应用可执行文件。 */
  execPath: string;
}

/**
 * 程序目录：开发态取仓库根目录，打包态取可执行文件所在目录。
 * 后续支持用户配置输出目录时，只替换 resolveOutputsDir 这一个解析点。
 */
export function resolveProgramDir(input: ProgramPaths): string {
  return input.packaged ? path.dirname(input.execPath) : path.resolve(input.appPath, "..");
}

/** 任务产出的固定默认目录：<程序目录>/outputs。 */
export function resolveOutputsDir(input: ProgramPaths): string {
  return path.join(resolveProgramDir(input), "outputs");
}
