// 仅 e2e 测试构建可用的数据夹具（Playwright 显式设置 VITE_E2E_FIXTURES=1）。
// 产品界面没有“演示模式”入口或横幅；生产构建一律别名到 e2eSource.disabled.ts，
// 因此夹具模块不会进入正式产物。
import { fixtureApi as backend } from "./e2eFixtures";
import type { YaoMuApi } from "./types";

/**
 * e2e 仅统计测试夹具的业务调用，验证宿主首次 ready 前不读取 Core；
 * 只有 window 上显式存在 businessCalls 数组时记录。
 * 本模块只在 VITE_E2E_FIXTURES 的开发服务器解析，生产构建使用 disabled 版本。
 */
function recordFixtureCall(name: string): void {
  if (typeof window === "undefined") return;
  const scope = window as Window & {
    __yaomuMock?: { businessCalls?: string[] };
  };
  const control = scope.__yaomuMock;
  if (!control || !Array.isArray(control.businessCalls)) return;
  control.businessCalls.push(name);
}

export const fixtureApi: YaoMuApi | null = new Proxy(backend, {
  get(target, property, receiver) {
    const value = Reflect.get(target, property, receiver);
    if (typeof value !== "function") return value;
    return (...args: unknown[]) => {
      recordFixtureCall(String(property));
      return (value as (...inner: unknown[]) => unknown).apply(target, args);
    };
  },
});
