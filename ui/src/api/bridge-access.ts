/** Safe access to the preload bridge during bootstrap/module evaluation. */
export type DesktopApi = NonNullable<Window["yaomu"]>["api"];

export function safeYaomu(): Window["yaomu"] | undefined {
  try {
    return window.yaomu;
  } catch {
    return undefined;
  }
}

export function safeDesktopApi(): DesktopApi | undefined {
  try {
    return safeYaomu()?.api;
  } catch {
    return undefined;
  }
}

export function hasDesktopMethod(
  api: DesktopApi | undefined,
  name: keyof DesktopApi,
): boolean {
  try {
    return !!api && typeof api[name] === "function";
  } catch {
    return false;
  }
}
