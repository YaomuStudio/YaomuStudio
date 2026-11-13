import { ref, type Ref } from "vue";

/** 浏览器 mock 的相对展示路径；桌面端始终以主进程返回的 <程序目录>/outputs 为准。 */
const MOCK_OUTPUT_DIR = "outputs";

export interface DefaultOutputDir {
  outputDir: Ref<string>;
  resolve: () => Promise<string>;
}

/**
 * 任务产出的固定默认目录。目录由主进程创建并授权，Renderer 不自行拼接主机路径。
 * 后续支持用户配置时，只需让主进程返回配置后的目录。
 */
export function useDefaultOutputDir(): DefaultOutputDir {
  const desktopApi = typeof window !== "undefined" ? window.yaomu?.api : undefined;
  const outputDir = ref(desktopApi ? "" : MOCK_OUTPUT_DIR);
  let pending: Promise<string> | null = null;

  async function resolve(): Promise<string> {
    if (!desktopApi) return MOCK_OUTPUT_DIR;
    if (!pending) {
      pending = desktopApi
        .getDefaultOutputDir()
        .then((directory) => {
          outputDir.value = directory;
          return directory;
        })
        .finally(() => {
          pending = null;
        });
    }
    return pending;
  }

  return { outputDir, resolve };
}
