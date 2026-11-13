// 非 e2e 测试构建：普通开发 serve 和生产 build 都解析到这里。
// 不静态引用夹具模块，确保示例项目/假 READY 不会被打包或执行。
import type { YaoMuApi } from "./types";

export const fixtureApi: YaoMuApi | null = null;
