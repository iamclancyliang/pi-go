# #28 全屏 TUI 拆分方案

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


- **内容：**
  - 组件和容器接口、差分重绘、窗口大小变化时的处理、`CURSOR_MARKER` 与硬件光标；
  - 同步输出、bracketed paste、stdin 缓冲（`stdin-buffer.ts`）；
  - 宽字符和 ANSI 感知的截断与切片（`utils.ts`）；
  - 基础组件：text、spacer、box、truncated-text、loader。
- **完成标准：** VT 模拟器里的屏幕状态和预期一致；窗口变宽变窄都不会留下残影；中日韩字符和 emoji 宽度正确。

### T2：编辑器组件（对应 Pi 约 4200 行：`editor.ts`、`autocomplete.ts`、`input.ts`、`keybindings.ts` 等）

**进度：** T2 拆成三步。**T2a 已完成 2026-10-24**：按键解码照 Pi 的 `parseKey` 重写（Kitty CSI-u 含按键事件类型和备用键、xterm modifyOtherKeys、rxvt、Linux 控制台、功能键），同一串字节在 Pi 里能匹配的几个名字都保留为别名；47 个 `tui.*` 动作的完整按键表和管理器（用户配置替换默认值、冲突检测、按修饰键集合匹配）；终端启动时协商 Kitty 键盘协议，不支持时退回 modifyOtherKeys，退出时还原。**T2b 已完成 2026-10-24**：`internal/tui/editor.go` 照 Pi 的 `Editor` 实现多行编辑器组件——上下两条横线、按词换行（CJK 字符两侧可断）、可见高度为终端的 30%（至少 5 行）并在横线上显示上下还有几行、焦点时输出光标标记；全部编辑动作（字形簇为单位的光标移动和删除、按词移动和删除、Emacs 式 kill/yank/yank-pop 及连续删除累积、fish 式撤销合并、Ctrl+] 跳到字符、翻页、粘性列及其决策表、历史记录 100 条和草稿恢复、反斜杠加回车换行）；大段粘贴（超过 10 行或 1000 个字符）折叠成 `[paste #N ...]` 标记，光标和删除把标记当作一个整体，提交时展开。Pi 的 `editor.test.ts` 中除自动补全外的用例已移植为 Go 测试，列号用字节而非 UTF-16 下标。一处已知差异：Pi 用 ICU 词典切分中文、日文词，Go 没有词典，这里把连续的汉字和假名当作一个词——对 Pi 测试里的文本（如“你好，世界”）结果一致，长串不带标点的中文按词移动会比 Pi 跨得更远。原来的单行编辑器改名为 `LineEditor`，仍供行模式使用。**T2c 已完成 2026-10-24**：模糊匹配（`FuzzyMatch`/`FuzzyFilter`）、选择列表组件 `SelectList`（主列宽度自适应、描述对齐、滚动位置提示、上下循环）、`CombinedAutocompleteProvider`（`/` 命令名模糊匹配及参数补全、路径补全含 `./` `~/` 和带空格时加引号、`@` 文件提及），编辑器接入补全：`/` 在行首、`@` `#` 及提供者自定义触发字符在词首时自动弹出，`@` 等触发后停顿 20ms 再查询，Tab 强制补全路径且唯一结果直接填入，列表打开时上下选择、Tab 采纳、Enter 采纳（命令名采纳后继续提交）、Esc 关闭，光标移动和删除后重新查询，过期请求取消；在界面里查询在后台协程运行，结果回到界面循环再应用。Pi 的 `@` 搜索调用外部的 fd，这里由 `tools.SearchPaths` 用纯 Go 实现同样的规则（含隐藏文件、排除 `.git`、跟随符号链接、仓库内应用 `.gitignore`、大小写智能匹配、含 `/` 时匹配完整路径）；fd 并行遍历、前 100 个结果顺序不固定，这里按广度优先，结果确定且浅层优先。Pi 的 `autocomplete.test.ts`、`fuzzy.test.ts`、`select-list.test.ts` 及 `editor.test.ts` 的补全用例已移植。命令列表和 `SearchPaths` 的接线在 T3 随交互模式一起做。

- **内容：**
  - 把现有的 `internal/tui.Editor` 做成组件：多行编辑、粘贴（大段粘贴折叠成标记）；
  - 自动补全：斜杠命令、`@` 文件路径（`autocomplete.ts`、`fuzzy.ts`）；
  - 补全 47 个按键动作的完整表（`keybindings.ts`）。
- **完成标准：** 现有编辑器测试全部迁移过来；补全和粘贴有测试；`/hotkeys` 列出完整的按键表。

### T3：对话渲染，做完后切换默认界面（对应 Pi 约 3000 行）

**进度：** T3 拆成三步：T3a Markdown 组件和配色，T3b 消息与工具组件，T3c 接入交互模式并切换默认界面。**T3a 已完成 2026-10-26**：`internal/tui/markdown.go` 照 Pi 的 `Markdown` 组件实现（标题、强调、删除线、行内代码、链接、列表含任务和嵌套、代码块、引用、分隔线、表格含窄宽度折行、HTML 原样显示）；终端能力检测照 Pi 的 `detectCapabilities`（决定链接用 OSC 8 超链接还是在后面打印地址，tmux 下询问是否转发）。验证方法是对照 Pi 自己的输出：`internal/tui/testdata/markdown/generate.ts` 用固定版本的 Pi 源码在 node 下渲染 106 段 Markdown（含流式输出中途的半截内容），在不同宽度、三种默认样式和有无超链接下共 828 个用例，Go 实现逐字节一致。Pi 用 marked 解析、这里用 goldmark，两者对边角情况的读法不同，凡是影响 Pi 输出的地方（块之间的空行、行尾空格、表格里的 `\|`、还没写完的网址和闭合围栏、链接引用定义）都按 marked 的行为补上了。新增 `internal/interactive` 包放交互模式的组件，先放主题：Pi 内置 dark 主题的全部颜色，真彩色和 256 色两种模式（256 色的就近取色照搬 Pi 的算法），以及 chalk 处理嵌套样式和换行的方式，同样用 Pi 生成的对照数据（`internal/interactive/testdata/theme/`）逐项核对。两处已知缺口：代码块的语法高亮（Pi 用 highlight.js，这里先统一用代码块颜色，和 Pi 遇到不认识的语言时一样）和 LaTeX 渲染（第 4 节已推后），都需要你决定是否引入依赖来做。**T3b 已完成 2026-10-26**：分两部分提交。第一部分是数据：工具结果新增 `Details`（只给界面看、不发给模型），经运行时放进 `tool_result` 事件的不序列化字段，已发布的事件流格式不变；edit 返回 Pi 格式的显示用 diff（带行号和上下文），并提供 `PreviewEdit` 在执行前算出 diff；read、grep、find、ls、bash 返回截断信息和触及的上限。diff 由新包 `internal/textdiff` 计算，它是 Pi 所用 jsdiff 8.0.4 行级、词级 diff 的移植（变更有多种对齐方式时选哪一种会体现在每个 diff 上，所以照搬算法而不是换一个库），对照 jsdiff 本身和 Pi 的 `generateDiffString` 的输出逐项核对。第二部分是 `internal/interactive` 里的组件：用户消息（背景色、保留列表标记和转义、OSC 133 区段标记）、assistant 消息（正文 Markdown、思考块灰色斜体或折叠成 “Thinking...”、截断/中止/出错的提示）、压缩摘要（折叠/展开）、工具执行框（待定/成功/失败三种底色）及 read、write、edit、bash、grep、find、ls 七个工具各自的渲染（edit 先显示预览 diff、逐词高亮变化处，bash 折叠为末 5 行并显示用时，各工具的截断警告和 “ctrl+o to expand” 提示），未知工具显示参数 JSON 和输出；Pi 的全部 `app.*` 按键表也已加入，提示里的按键名随用户配置变化，Mac 上 alt 显示为 option。验证方法同 T3a：用固定版本 Pi 的组件在 node 下渲染 97 个场景，Go 实现除 2 个外逐字节一致——那 2 个是 edit 无法应用时的错误提示，pi-go 的 edit 工具报错措辞本来就和 Pi 不同（这影响的是模型看到的文字，不在界面范围内，另行处理）。已知缺口：Pi 在命令运行中会流式显示 bash 输出（`tool_execution_update`），pi-go 运行时还没有这类事件，所以运行中只显示计时；代码高亮未移植，highlight.js 认识的语言先按无色正文显示（Pi 里正文大部分本就无色，只少了关键字和字符串的颜色）；read 对 Pi 自带文档的紧凑显示不适用于 pi-go；图片不显示。**T3c 第一部分已完成 2026-10-26，交互模式默认改用新界面**：`internal/interactive/mode.go` 照 Pi 的 regular 模式布局（对话、待发送消息、状态行、编辑器、底栏），assistant 回复按块流式显示，工具调用在出现时就画出、执行时开始计时、结果到达后换成成功或失败的底色，压缩和自动重试显示各自的状态行；Enter 在空闲时发送、运行中转为 steer，alt+enter 为 follow-up，排队的消息显示在编辑器上方，送达模型时移入对话；Esc 中断运行并把未送达的排队消息放回编辑器，Ctrl+C 清空编辑器、连按两次退出，Ctrl+D 在编辑器为空时退出，ctrl+o 展开工具输出和启动帮助，ctrl+t 折叠思考；`/` 命令和 `!` shell 命令沿用现有实现，输出打印进对话，需要确认的命令把下一次输入当作回答；启动时画出已有对话（含压缩摘要和工具调用结果），命令行给出的提示词依次发送，退出时打印恢复会话的命令。底栏显示工作目录（git 分支、会话名）、token 合计、上下文占比和模型。`cmd/pi` 在终端里默认进入新界面，`TERM` 为空或 `dumb`、打不开终端设备、或设置 `PI_GO_LINE_MODE=1` 时退回按行交互（pi-go 自己的设计，N 类，理由写在 parity matrix）。用假模型服务在伪终端里跑过完整一轮（输入、流式回复、Markdown、底栏、退出），模式逻辑另有 Go 测试。第二部分待做：bash `!` 命令输出流式显示（Pi 的 BashExecutionComponent）、取回排队消息（dequeue）、底栏的 provider 前缀和费用。另外发现两处既有问题：pi-go 的 Ollama provider 无法处理工具调用（eino 的转换不带位置，已另开任务），以及 edit 工具报错措辞与 Pi 不同。

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
