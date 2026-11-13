# 共享 UI 原语组件

所有组件用 CSS 变量（`styles/tokens.css`），不写死颜色。props 类型见各自 SFC。

| 组件 | 用途 | 关键 props |
|---|---|---|
| `YmIcon` | 内联 SVG 图标 | `name`, `size?` |
| `YmSpinner` | 加载转圈 | `size?` |
| `YmButton` | 按钮 | `variant?`(primary/secondary/ghost/danger), `size?`(sm/md/lg), `icon?`, `disabled?`, `loading?`, `block?` |
| `YmIconButton` | 图标按钮（无障碍） | `name`, `label`, `size?`, `disabled?`, `variant?` |
| `YmField` | 表单字段包裹（label/hint/error） | `label?`, `hint?`, `error?`, `required?`；默认插槽 |
| `YmTextInput` | 单行输入 | `modelValue`, `placeholder?`, `disabled?`, `type?` |
| `YmTextArea` | 多行输入 | `modelValue`, `rows?`, `placeholder?`, `disabled?` |
| `YmSelect` | 下拉 | `modelValue`, `options: {value,label}[]`, `placeholder?`, `disabled?` |
| `YmSwitch` | 开关 | `modelValue`, `disabled?`, `label?` |
| `YmCheckbox` | 复选 | `modelValue`, `label?`, `disabled?` |
| `YmRadioGroup` | 单选组 | `modelValue`, `options: {value,label}[]`, `disabled?` |
| `YmSegmented` | 分段选择 | `modelValue`, `options: {value,label}[]` |
| `YmBadge` | 状态徽标 | `tone?`(neutral/success/warning/error/info/accent)，默认插槽文本 |
| `YmProgress` | 进度条 | `value?`(0-100；缺失为未知), `tone?`, `indeterminate?`（未知/等待中，不伪造 0%） |
| `YmEmpty` | 空态 | `title?`, `description?`, `icon?`，`#action` 插槽 |
| `YmCard` | 卡片容器 | `#header`/默认插槽 |
| `YmStats` | 统计条/卡片 | `items: {value,label,dir?}[]`, `variant?`(inline/card), `loading?` |
| `YmPanel` | 可折叠面板 | `title`, `collapsible?`, `defaultOpen?`，默认插槽 |
| `YmStepper` | 步骤条 | `steps: {key,label}[]`, `current`(key), `disabled?` |
| `YmTabs` | 页签 | `modelValue`, `tabs: {value,label}[]` |
| `YmDialog` | 确认对话框（仅确认/告警，不用于创建） | `modelValue`, `title?`, `confirmText?`, `cancelText?`, `danger?`；`#footer` 可覆盖 |
| `YmTooltip` | 图标/文字气泡（hover + keyboard focus） | `label`, `disabled?`, `placement?`(right/left), `delay?`；默认插槽为唯一触发元素 |

状态→文案/色调统一走 `@/domain/status`（`taskStatusLabelKey/tone` 等）。

`YmTooltip` 约定：Teleport 到 body + fixed 定位，避免被侧栏/滚动容器裁切；hover 有 120ms 延迟，键盘 focus 立即显示；Escape、失焦、移出、滚动或窗口 resize 关闭；tooltip 只做视觉补充，触发元素必须保留 `aria-label` 或可访问文本。图标类触发元素优先使用 `YmIconButton` 的 `label`。

图标可用名（`YmIcon`）：`home folder list film mic eraser layers globe subtitles link gear play play-round pause stop check x chevron-down chevron-right chevron-left plus refresh edit trash download upload folder-open search info alert volume wave external more box shield cpu disk bell moon sun`.
