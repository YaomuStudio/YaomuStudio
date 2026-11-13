/** 播放器输入：地址必须来自桌面桥的 assetUrl，不接受任意 URL。 */
export interface PlayerSource {
  /** 逻辑播放 key（segment:... / candidate:... / preview:<choice>），用于单实例互斥。 */
  key: string;
  /** `yaomu-asset://asset/<assetId>`；由 preload 生成，不含令牌。 */
  src: string;
  kind: "audio" | "video";
  /** 06 单句时间窗；未提供时播放整段。 */
  startMs?: number;
  endMs?: number;
  /** A/B 切换时恢复到的时间位置（毫秒）；未提供时从 startMs 开始。 */
  seekMs?: number;
}
