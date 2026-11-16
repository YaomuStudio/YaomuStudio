/**
 * 更新通道选择：国内优先对象存储 feed，国际优先 GitHub Releases。
 *
 * 构建产物 app-update.yml 默认指向对象存储；Main 在打包态启动时按系统区域覆盖。
 * 首选源检查失败时，Main 会在同一会话内切换到备用源一次。
 */
export type UpdateFeed =
  | { id: "domestic"; provider: "generic"; url: string }
  | { id: "international"; provider: "github"; owner: string; repo: string };

/** 公开对象存储 feed；必须与 desktop/electron-builder.yml 的 publish.url 一致。 */
export const DOMESTIC_UPDATE_FEED_URL = "https://mls-long.yaomuai.com/mls/yaomustudio/";

export const GITHUB_UPDATE_FEED = {
  owner: "YaomuStudio",
  repo: "YaomuStudio",
} as const;

const DOMESTIC_FEED: UpdateFeed = {
  id: "domestic",
  provider: "generic",
  url: DOMESTIC_UPDATE_FEED_URL,
};

const INTERNATIONAL_FEED: UpdateFeed = {
  id: "international",
  provider: "github",
  owner: GITHUB_UPDATE_FEED.owner,
  repo: GITHUB_UPDATE_FEED.repo,
};

/**
 * 国内判定：简体中文区域或中国标准时间。语言/时区都不是网络质量证明；
 * 首选源失败时由 Main 回退到另一条通道。台湾/香港等繁中区域按国际通道处理。
 */
export function selectUpdateFeeds(
  locale: string,
  timeZone = "",
): { primary: UpdateFeed; fallback: UpdateFeed } {
  const normalizedLocale = String(locale ?? "").toLowerCase();
  const normalizedZone = String(timeZone ?? "");
  const domestic =
    /^zh-(cn|hans)/.test(normalizedLocale) ||
    normalizedZone === "Asia/Shanghai" ||
    normalizedZone === "Asia/Chongqing" ||
    normalizedZone === "Asia/Urumqi";
  return domestic
    ? { primary: DOMESTIC_FEED, fallback: INTERNATIONAL_FEED }
    : { primary: INTERNATIONAL_FEED, fallback: DOMESTIC_FEED };
}
