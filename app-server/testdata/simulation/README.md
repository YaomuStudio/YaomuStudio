# 外部仿真全流程 fixture

`full-flow.json` + 两份 SRT 是 `TestSimulationFullFlow` 的唯一流程输入。
测试从磁盘读取它们，不在 Go 代码里硬编码字幕正文。

## 边界

- **仅测试侧**：`internal/scheduler/simulation_full_flow_test.go` 里的调度驱动
  代替尚未实现的“Run 节点自动展开为 Task”，生产 Core 行为未改。
- **模型执行是假的**：`simulationExecutor` 为八个节点写固定占位产物，不构造
  任何 FlashCLI 参数，不绕过生产门控。
- **导出是真的**：导出任务仍走 Core → Python Runner → FFmpeg；有工具时校验
  MP4/WAV/MP3 的实际容器与流，SRT/ASS 校验 fixture 文本与时间轴。
- 没有 FFmpeg/Python 时，八节点、两次审核、候选/选用和 SRT/ASS 仍会执行；
  媒体导出显式 `SIMULATION_MEDIA_EXPORT_SKIPPED`，不伪装为通过。

## 输入文件

| 文件 | 用途 |
|---|---|
| `full-flow.json` | 项目名、源/目标语言、提取/擦除模式、媒体规格、八节点顺序 |
| `source.zh.srt` | 04 提取后写入源字幕轨的台词与时间轴 |
| `translated.en.srt` | 06 翻译后写入目标字幕轨、并用于导出断言的译文 |

两份 SRT 的 cue 数量和每条时间轴必须一致，否则测试直接失败。改动台词时保留
时间轴即可；改动时间轴时同步改另一份。

## 运行

```bash
cd app-server
FFMPEG_BIN=/path/to/ffmpeg \
FFPROBE_BIN=/path/to/ffprobe \
PYTHON_BIN=/path/to/python3 \
go test ./internal/scheduler -run TestSimulationFullFlow -count=1 -v
```

Windows PowerShell 使用 `$env:FFMPEG_BIN = '...'` 后再执行 `go test`。若工具已在
`PATH`，可省略对应环境变量。`PYTHON_BIN` 也会被现有 Runner 集成测试使用；
Windows 上请指向真实 Python，不要指向 Microsoft Store 的 `WindowsApps` 占位符。

## 覆盖链

1. `CreateWorkflowRun` 生成八节点；
2. 依次创建并执行 01–08 节点任务（06/08 进入 `waiting_review`）；
3. 04 后写入源字幕轨，06 后写入译文轨并批准翻译审核；
4. 07 后生成 WAV 候选音频并设置逐句选用；
5. 08 后批准交付审核，Run 收敛为 `SUCCEEDED`；
6. 导出 SRT、ASS、MP4、WAV、MP3，校验导出记录与事件链。

## 已知限制

- 该测试证明“Core 业务状态机 + 调度 + 审核 + 导出”在外部输入下可串联，
  **不证明生产环境已经能自动跑全流程**：生产 Core 仍只创建节点，不会自动
  创建节点 Task；模型安装/下载与 FlashCLI 执行仍受外部门槛约束。
- 中文 ASR、翻译、TTS、克隆、擦除、分离的真实模型输出未参与；节点产物是
  固定的 `simulated-<taskID>.txt`。
