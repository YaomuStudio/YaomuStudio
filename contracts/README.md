# 契约层（contracts）

语言无关的跨进程契约，是 `desktop` / `ui` / `app-server` / `runner` 共同对齐的**唯一来源**。

权威细节见 `docs/`：

- `docs/local-api-contract.md` — 内部 API 与执行协议
- `docs/data-and-storage.md` — 领域模型与本地存储
- `docs/model-runtime-manifest.md` — 模型与运行时清单
- `docs/architecture.md` — 职责与协议边界

- `desktop-runtime.md` / `desktop-runtime.schema.json` — Main/preload/Renderer 的宿主启动状态白名单桥；独立于Core业务HTTP/SSE，契约先行，状态见对应实现记录。

规则：改动先改契约与 `docs/`，再改实现；实现不得自行发明契约外的字段或枚举。
