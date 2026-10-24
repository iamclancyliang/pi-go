# #28 全屏 TUI 拆分方案（草案）

**状态：** 已认可（2026-10-24，仓库所有者按第 6 节的建议拍板） · **日期：** 2026-10-24 · **基线：** `earendil-works/pi@086c32e`

## 1. 要移植的是什么

| 部分 | Pi 源码 | 规模 |
| --- | --- | --- |
| TUI 库 | `packages/tui/src` | 约 1.67 万行，39 个文件 |
| 交互模式 | `src/modes/interactive/interactive-mode.ts` | 6448 行 |
| 交互组件 | `src/modes/interactive/components/` | 约 9000 行，45 个文件 |
| 主题 | `src/modes/interactive/theme/` | 约 2000 行，含 dark/light 两套主题 |
| 文档 | `docs/tui.md`、`keybindings.md`、`themes.md`、`terminal-setup.md` | 共 1661 行 |

**影响拆分的四个事实（都来自固定版本的源码）：**

1. **Pi 有两种界面模式，默认是 `regular`。**
   - `regular`（`tui-main-screen.ts`，586 行）：对话直接写进终端的滚动历史，只重绘变化的行。
   - `fullscreen`（`tui-alt-screen.ts`，1314 行，外加滚动视图、布局、搜索）：在设置里明确标注为 "experimental"，通过 `--tui-mode` 或 `tuiMode` 设置切换。
   - 所以先做 `regular`，`fullscreen` 放到最后。
2. **Pi 的渲染模型很简单：** 每个组件实现 `render(width) → string[]`，由 TUI 对比新旧行做差分输出（`tui.ts`）。获得焦点的组件在光标位置输出一个 `CURSOR_MARKER`，TUI 再把硬件光标挪到那里，这样输入法候选窗口才能定位正确。这套模型不依赖任何 TUI 框架，可以用 Go 原样实现。
3. **Pi 的 TUI 只依赖两个库：** `marked`（Markdown 解析）和 `get-east-asian-width`（计算东亚字符宽度）。pi-go 已经有 goldmark，宽度可以用 `golang.org/x/text/width` 或 `rivo/uniseg`。
4. **pi-go 现有的 TUI 只有输入层**（`internal/tui`，约 800 行）：按键解码、行编辑器（含 kill ring、undo、历史），以及 Pi 按键表的一部分。上面是一个按行交互的循环（D-7）。

**仓库规则的约束：** 特性清单里 TUI 这一项目前是 `semantics-needed`。按 AGENTS.md 的规定，在把语义从固定版本的源码读出来并记录之前，不能开始实现。所以每个阶段都要先调研、再实现。

## 2. 需要先定的事：ADR-0011，TUI 自己实现还是用框架

| 方案 | 优点 | 问题 |
| --- | --- | --- |
| **A. 照 Pi 的模型自己实现**（组件 `render(width)` + 差分重绘 + `CURSOR_MARKER`） | 和 Pi 的语义一一对应，包括写进滚动历史的方式、硬件光标、覆盖层；依赖少；方便测试（渲染结果就是字符串数组） | 终端兼容性要自己处理（同步输出、bracketed paste、Kitty 键盘协议、宽字符） |
| B. bubbletea + lipgloss | 成熟，社区大 | 它有自己的事件循环和 Elm 式状态模型。inline 模式只重绘视图区域，不是 Pi 那样"整段对话进滚动历史、只改变化的行"。要做到同样的效果，得绕开框架本身 |
| C. tcell / tview | 擅长全屏 | 天生就是 alt-screen，和 Pi 默认的 regular 模式正好相反 |

**建议选 A。** 理由和 ADR-0002 的思路一致：用户看得到的界面行为是产品契约，不该交给一个语义对不上的框架去决定。新增依赖只需要一个处理字符宽度的库。这份 ADR 应该在阶段 T0 写好并由你认可。

## 3. 阶段划分

每个阶段都要满足四点：**能单独合并**、**先调研再实现**、**有自动化测试**、**在 parity matrix 里有对应的行**。

### T0：调研和 ADR（不写产品代码）

- 读 `docs/tui.md`、`keybindings.md`、`terminal-setup.md`，以及 `tui.ts` 和 `tui-main-screen.ts` 的渲染语义，补齐特性清单 §11，把 `semantics-needed` 推进到 `enumerated`。
- 写 ADR-0011（渲染器由谁实现）。
- 定测试方法：渲染器写到 `io.Writer`，测试用一个最小的 VT 模拟器把输出还原成屏幕网格再断言。这台机器上没有 tmux，所以不能靠截屏比对。
- **完成标准：** 清单更新、ADR 获你认可、测试工具的设计确定。

### T1：渲染核心（对应 Pi 约 4300 行：`tui.ts`、`tui-main-screen.ts`、`terminal.ts`、`utils.ts`、`stdin-buffer.ts` 和基础组件）

- **内容：**
  - 组件和容器接口、差分重绘、窗口大小变化时的处理、`CURSOR_MARKER` 与硬件光标；
  - 同步输出、bracketed paste、stdin 缓冲（`stdin-buffer.ts`）；
  - 宽字符和 ANSI 感知的截断与切片（`utils.ts`）；
  - 基础组件：text、spacer、box、truncated-text、loader。
- **完成标准：** VT 模拟器里的屏幕状态和预期一致；窗口变宽变窄都不会留下残影；中日韩字符和 emoji 宽度正确。

### T2：编辑器组件（对应 Pi 约 4200 行：`editor.ts`、`autocomplete.ts`、`input.ts`、`keybindings.ts` 等）

- **内容：**
  - 把现有的 `internal/tui.Editor` 做成组件：多行编辑、粘贴（大段粘贴折叠成标记）；
  - 自动补全：斜杠命令、`@` 文件路径（`autocomplete.ts`、`fuzzy.ts`）；
  - 补全 47 个按键动作的完整表（`keybindings.ts`）。
- **完成标准：** 现有编辑器测试全部迁移过来；补全和粘贴有测试；`/hotkeys` 列出完整的按键表。

### T3：对话渲染，做完后切换默认界面（对应 Pi 约 3000 行）

- **内容：**
  - Markdown 组件（`markdown.ts`）：用 goldmark 的语法树渲染成 ANSI；
  - 用户消息、assistant 消息的流式更新（接到 runtime 现有的 `ReplyObserver` 上）、思考块的折叠；
  - 工具执行组件，包括展开/收起、edit 工具的 diff、bash 输出；
  - 压缩摘要、底部状态栏（模型、token、上下文占比、工作目录）、"处理中"提示；
  - 待发送消息区：steer 和 follow-up 的队列，以及取回队列（dequeue）。
- **完成标准：** 交互模式默认改用新 TUI（`regular`），D-7 关闭。在不支持的终端（如 `TERM=dumb`）上保留现在的按行交互作为后备，这一点算 pi-go 自己的设计（N 类），要单独说明理由。

### T4：各种选择器，每个可以单独合并（对应 Pi 约 5800 行，其中 `tree-selector.ts` 1427 行、`session-selector.ts` 1031 行）

- **先做的通用部分：** select-list、settings-list、input、覆盖层和组件栈。
- **依次接入：**
  1. `/model` 和 scoped-models；
  2. `/resume`，含会话搜索；
  3. `/tree`，同时完成 #32 剩下的标签和分支摘要；
  4. `/fork` 和 RPC 的 `get_fork_messages`，做完后 RPC 32 条命令全部实现；
  5. `/settings`；
  6. `/login`、`/logout`；
  7. `/trust`；
  8. 思考等级选择器。
- **完成标准：** 每个选择器都能用键盘完成选择和取消，并有屏幕快照测试。

### T5：应用级快捷键和其余功能（对应 Pi 约 1500 行）

- **内容：**
  - `app.*` 动作：中断、清空（连按两次 Ctrl-C 退出）、挂起（Ctrl-Z）、切换思考等级、选择模型、展开工具输出、显示或隐藏思考过程、外部编辑器（`$VISUAL` / `$EDITOR`）、复制、取回队列、新建/树/分叉/恢复会话；
  - 启动页头部；
  - 用户自定义按键（`keybindings.json`）。
- **完成标准：** 每个 `app.*` 动作有测试；#28 关闭。

### T6：主题（Themes 是清单里单独的一项，建议另开 issue）

- dark 和 light 两套内置主题、主题文件格式、主题选择器，以及通过 OSC 11 查询终端背景色。
- 在 T6 之前，T1–T5 统一用一套写死的配色。

### T7：fullscreen 模式（Pi 里还是实验性的，放最后，建议另开 issue）

- alt-screen、滚动视图、布局、搜索、滚动条，以及 `--tui-mode` 和相关设置。

## 4. 明确推后的内容

| 内容 | 原因 |
| --- | --- |
| 终端内显示图片（`terminal-image.ts`、`image.ts`，Kitty / iTerm 协议） | pi-go 目前没有图片输入 |
| LaTeX 渲染（`latex.ts`，1380 行）、Mermaid | 体量大、用户少，需要你决定要不要做 |
| 扩展用的 UI 组件（extension-editor / selector / input） | 等扩展系统 |
| 彩蛋和公告（armin、daxnuts、clankolas、earendil-announcement） | 不影响功能。记成"已接受的偏差"还是照搬，需要你定 |

## 5. 顺序和工作量

```
T0 ─► T1 ─► T2 ─► T3 ─┬─► T4（每个选择器可以并行）
                      └─► T5
T6、T7 在 T3 之后随时可以做，彼此独立
```

- **关键路径是 T0 → T3。** 走完之后用户就能用上真正的界面，D-7 关闭。
- **按 Pi 的代码量粗估，T1–T5 约 1.8 万行 TS。** Go 版本预计相当，测试另算。
- **建议把 #28 拆成子 issue：** T0–T5 各开一个，再给 T6、T7 各开一个独立 issue。现在 `gh` 的写操作被权限拦着，需要你放开或者你自己来建。

## 6. 已定的事（2026-10-24）

1. **ADR-0011 采用方案 A**：照 Pi 的模型（组件 `render(width)` + 差分重绘 + `CURSOR_MARKER`）自己实现，不用框架。
2. **T3 之后保留按行交互**，作为不支持新界面的终端的后备。这是 pi-go 自己的设计（N 类），在 T3 里写明理由。
3. **LaTeX、Mermaid、彩蛋：推后，暂不定性。** 等 T5 完成后再决定是照做，还是记成已接受的偏差；在那之前，它们在 parity matrix 里保持 `incomplete`。
4. **子 issue：** 按 T0–T5 各开一个，T6、T7 单独开。`gh` 的写权限放开之前，进度先记在本文件和 parity matrix 里。
