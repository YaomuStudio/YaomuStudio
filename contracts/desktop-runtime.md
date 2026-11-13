# Desktop Runtime Bridge v1

状态：2026-11-08已实现并完成本机受控Electron/Core与契约回归；Windows/WSL实机门槛仍未验证。适用于Electron Main ↔ preload ↔ Renderer；不是Core HTTP端点，不写业务SSE或持久游标。快照形状唯一来源为 `desktop-runtime.schema.json`。实现状态见契约变更记录。

## 白名单接口

```ts
window.yaomu.runtime = {
  getSnapshot(): Promise<RuntimeResult>,
  onChanged(callback: (snapshot: RuntimeSnapshot) => void): () => void,
  retry(): Promise<RuntimeResult>,
};
type RuntimeResult =
  | { ok: true; data: RuntimeSnapshot }
  | { ok: false; error: {
      code: "LOCAL_ACCESS_DENIED" | "CORE_NOT_READY";
      messageKey: "api.accessDenied" | "api.coreNotReady";
      retryable: boolean;
      actionIds: [];
    } };
```

通道固定：`yaomu:runtime:get`、`yaomu:runtime:retry`（invoke），`yaomu:runtime:attach`、`yaomu:runtime:detach`（send），`yaomu:runtime:changed`（Main→preload通知）。不接受Renderer给出的状态、版本、任意通道、命令或路径。

- 每个Main handler在读状态/登记订阅/启动前调用同一个DesktopAuthorization.requireSender；受信顶层应用文档外一律拒绝。invoke返回安全拒绝信封；send拒绝时不回发。
- preload先登记changed listener再发送attach，返回同步且幂等unsubscribe（移除本listener并detach）。同窗多订阅引用计数，destroyed/导航离开应用文档清理；广播时再核来源。Main只向已授权订阅者推送。
- Renderer必须先onChanged，再getSnapshot，按revision只接受更高版本；相同版本幂等，低版本/非法shape忽略。不能用Date.now或本地业务epoch代替Main版本。

## 快照语义

| 字段 | 约束 |
|---|---|
| schemaVersion | 固定1 |
| revision | Main生命周期内安全正整数，每次可观察状态变化递增；不得倒退 |
| generation | 已确认Core连接代次，初始0，每接受一个不同兼容Core实例递增；不是Core.server_epoch或SSE游标 |
| phase | preparing / starting / ready / restarting / failed / stopping / stopped |
| reason | 非failed必须null；failed为有限原因，不包含任意错误字符串 |
| canRetry | 非failed必须false；未确认清理/协议或workspace不匹配必须false；普通已确认清理失败可true |

初值preparing/revision=1/generation=0。准备WSL不等于Core或模型就绪。ready只在Main经私有连接完成 `GET /api/v1/system` 握手后发布，要求app_id=`yaomu`、protocol_version=`0.2`、core_ready=true、非空instance_id/workspace_id及非负安全整数server_epoch。请求必须受本次启动AbortSignal和有界超时约束；不把health/live单独当业务ready。

Main在首个握手锁定workspace_id，同会话新Core若workspace变化→workspace_mismatch，拒绝连接、要求重启应用，不复用旧SSE游标。相同workspace不同instance允许新generation。整个握手保留CoreConnection仅Main内存；快照禁止端口/URL/token、workspace路径、完整env/argv、媒体/字幕正文或原始异常。

## 重试与退出

- retry在preparing/starting/restarting/ready时仅返回当前快照，不新开启动。
- failed且canRetry=true时进入同一个Core拥有者的新尝试，立即返回已递增revision的starting/preparing快照；完成随后推送。并发retry合流，不跨过Supervisor隔离。
- failed且canRetry=false、stopping/stopped时只返回当前快照，不修改任何业务数据、不自动安装/重启WSL。
- 取消/退出先禁止新retry，取消准备/启动并沿既有有界退出；迟到握手不能ready或创建窗口。未确认cleanup记录失败且不可重试，不能新建Supervisor绕过。
- 通道只重试宿主初始化，不恢复Run/Task、不自动发送保存/批准/安装等业务写请求。

## Renderer加载与恢复

首次ready前显示可交互初始化页，支持本地语言/主题与安全retry；不挂载业务页、不发送初始化业务读取。保留请求路由；首次ready后正常挂载并读Core。旧完整桥无runtime接口时维持原加载行为，不无限等、不落mock；显式dev demo/无桥模式维持现有守卫。

首次ready后，不因掉线改变RouterView key或重挂组件。只读全局缓存和干净页面按新generation刷新；dirty/saving编辑器保留草稿与base revision，显示恢复提示，不自动提交或覆盖。旧generation请求返回不得写回当前缓存/页面。相同workspace沿既有持久事件高水位握手；runtime revision/generation与seq=0绝不推进业务游标。

## 必测

- IPC未注册/子帧/错误文档get/retry/attach拒绝，无副作用；导航/销毁后无推送。
- changed先于旧snapshot回包仍不回退；unsubscribe无后续回调。
- 慢初始化首窗及时、交互响应，ready后真实数据自动出现；失败重试不双Core。
- 同workspace重启/不同workspace拒绝；dirty文本/修订base保留、旧读回包失效、无自动写请求。
- 真实隔离Electron+Core证明权限与加载；合成Runner/WSL延迟不冒称Windows或模型生产验收。
