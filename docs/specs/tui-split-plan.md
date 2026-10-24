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

### T0：调研和 ADR（不写产品代码）— **已完成 2026-10-24**

结果：特性清单 §11.4–11.6（regular 模式的渲染语义、终端会话、42 个 `app.*` 动作）；ADR-0011（照 Pi 的行模型自己实现，依赖 `rivo/uniseg` 和已有的 goldmark）；测试方法定为“渲染输出喂给测试里的小型终端模拟器，断言屏幕和滚动历史”。

- 读 `docs/tui.md`、`keybindings.md`、`terminal-setup.md`，以及 `tui.ts` 和 `tui-main-screen.ts` 的渲染语义，补齐特性清单 §11，把 `semantics-needed` 推进到 `enumerated`。
- 写 ADR-0011（渲染器由谁实现）。
- 定测试方法：渲染器写到 `io.Writer`，测试用一个最小的 VT 模拟器把输出还原成屏幕网格再断言。这台机器上没有 tmux，所以不能靠截屏比对。
- **完成标准：** 清单更新、ADR 获你认可、测试工具的设计确定。

### T1：渲染核心（对应 Pi 约 4300 行：`tui.ts`、`tui-main-screen.ts`、`terminal.ts`、`utils.ts`、`stdin-buffer.ts` 和基础组件）

**已完成 2026-10-24。** 交付：`internal/tui` 里的宽度、截断、换行（`width.go`、`ansi.go`，按 Pi 的字形宽度规则，包括印地语系连写、缅甸语间距符号、泰语和老挝语 AM）；组件模型和 Text、Spacer、Box、TruncatedText、Loader（`component.go`）；regular 模式渲染器（`screen.go`，§11.4 的五种情况）；事件循环式的 UI（`ui.go`，16 毫秒节流、按键立即重画、Esc 超时、粘贴）；真实终端（`tty.go`，raw 模式、bracketed paste、窗口大小变化、用 select(2) 读输入以便能停下）。测试：Pi 的宽度、截断、换行和渲染场景都移植成了 Go 测试，经测试终端断言屏幕和滚动历史；真实终端在伪终端里做了冒烟测试（`PI_GO_TTY_SMOKE=1`）。

**挪到后面阶段的：** Kitty 键盘协议协商移到 T2（和按键表一起做）；OSC 9;4 进度提示移到 T3（智能体工作时才用）；覆盖层仍在 T4。渲染器里"变化在屏幕底部以下时先往下滚"那个分支，正常的追加和收缩都走不到，没有测试覆盖，按 Pi 原样保留。