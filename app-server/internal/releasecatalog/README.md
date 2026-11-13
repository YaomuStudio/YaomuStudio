# 产品 interim 发布目录

`catalog.json` 是**未签名**的产品侧安装锁：逐文件 SHA-256 + 批准 CDN URL，供 Core 解析 `tts` / `voice-clone` / `separation` / `asr` 的安装计划。

- **不是** FlashHub 签名 `ReleaseManifest`，不能对外宣称已完成正式准入。
- FlashHub files API 只有 MD5；摘要由 `scripts/generate-release-catalog.py` 对 CDN 字节重算。
- 清单只锁 bundle 代码/运行时文件；模型权重仍由 flashcli 首次运行时从其缓存源获取（ASR 权重未预置）。
- Core 默认 `go:embed` 本目录；`YAOMU_RELEASE_CATALOG` 可覆盖路径，或设为 `none` 关闭。
- 分发前缀优先 `YAOMU_RELEASE_BASE_URLS`，否则用清单内 `distribution_prefixes`。
- 安装完成以全文件 SHA-256 校验作为 MVP 就绪门槛（无独立可执行 probe）；首次真实执行仍是最终证据。

刷新（需联网）：

```bash
python scripts/generate-release-catalog.py
```
