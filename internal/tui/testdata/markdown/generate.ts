// Regenerates golden.json from Pi's own Markdown component at the pinned
// revision, so the Go renderer is checked against what Pi draws rather than
// against a reading of its code.
//
//   git -C <pi checkout> archive 086c32e packages/tui | tar -x -C <dir>
//   ln -s <pi checkout>/node_modules <dir>/node_modules   # marked 18.0.5
//   PI_PIN=<dir> node internal/tui/testdata/markdown/generate.ts
//
// Node 22.6 or later runs the TypeScript directly.
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pin = process.env.PI_PIN;
if (!pin) throw new Error("set PI_PIN to the extracted pinned Pi tree");
const { Markdown } = await import(join(pin, "packages/tui/src/components/markdown.ts"));
const { setCapabilities } = await import(join(pin, "packages/tui/src/terminal-image.ts"));

const fg = (n: number) => (t: string) => `\x1b[38;5;${n}m${t}\x1b[39m`;
const theme = {
	heading: fg(1),
	link: fg(2),
	linkUrl: fg(3),
	code: fg(4),
	codeBlock: fg(5),
	codeBlockBorder: fg(6),
	quote: fg(7),
	quoteBorder: fg(8),
	hr: fg(9),
	listBullet: fg(10),
	bold: (t: string) => `\x1b[1m${t}\x1b[22m`,
	italic: (t: string) => `\x1b[3m${t}\x1b[23m`,
	strikethrough: (t: string) => `\x1b[9m${t}\x1b[29m`,
	underline: (t: string) => `\x1b[4m${t}\x1b[24m`,
};
const styles: Record<string, object | undefined> = {
	none: undefined,
	thinking: { color: fg(11), italic: true },
	user: { color: fg(12), bgColor: (t: string) => `\x1b[48;5;13m${t}\x1b[49m` },
};

const texts: Record<string, string> = {
	empty: "",
	blank: "  \n\n ",
	plain: "Hello world",
	paragraphs: "First paragraph.\n\nSecond paragraph\ncontinues here.",
	manyBlank: "a\n\n\n\nb",
	leadingTrailing: "\n\nlead and trail\n\n",
	trailingNewline: "one line\n",
	headings: "# One\n## Two\n### Three\n#### Four\n##### Five\n###### Six\ntext",
	headingInline: "# A `code` and **bold** heading\n\nafter",
	setext: "Title\n=====\n\nSub\n---",
	emphasis: "Some **bold**, *italic*, ***both***, ~~struck~~, and `code` here.",
	strikeStrict: "~~ not struck~~ and ~single~ and ~~yes~~",
	nestedEmphasis: "**bold with *italic* inside** after",
	underscores: "snake_case_word and __bold__ and _em_",
	links: "A [link](https://example.com) and [same](https://same.org) text.",
	linkSame: "[https://example.com](https://example.com)",
	autolink: "Visit <https://example.com/x> or <me@example.com>.",
	bareUrl: "See https://example.com/path?q=1 for more and www.example.org too.",
	mailtoLink: "[me@x.org](mailto:me@x.org)",
	linkFormatted: "[**bold link**](https://x.y)",
	image: "![alt text](https://img.png)",
	unordered: "- one\n- two\n- three",
	ordered: "1. one\n2. two\n3. three",
	orderedStart: "7. seven\n8. eight",
	orderedParen: "1) one\n2) two",
	starPlus: "* star\n+ plus",
	nested: "- a\n  - b\n    - c\n  - d\n- e",
	nestedOrdered: "1. a\n   1. b\n   2. c\n2. d",
	tasks: "- [ ] todo\n- [x] done\n- plain",
	loose: "- a\n\n- b\n\n- c",
	listParagraphs: "- item one\n\n  continued paragraph\n- item two",
	listCode: "- item\n\n  ```\n  code\n  ```",
	listAfterParagraph: "Intro:\n- a\n- b\n\nAfter.",
	listLongItem: "- this list item is long enough that it has to wrap onto another line at narrow widths",
	emptyItem: "-\n- x",
	code: "```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```",
	codeNoLang: "```\nplain code\n  indented\n```",
	codeTilde: "~~~python\nprint(1)\n~~~",
	codeIndented: "    indented code\n    more",
	codeUnclosed: "```js\nconst a = 1;\nconst b",
	codePartialFence: "```js\nconst a = 1;\n``",
	codePartialFence1: "```\nx\n`",
	codeLong: "```\n" + "x".repeat(70) + "\n```",
	codeThenText: "```\nc\n```\ntext right after",
	textThenCode: "text\n```\nc\n```",
	quote: "> quoted text\n> more",
	quoteNested: "> outer\n>> inner",
	quoteList: "> - a\n> - b",
	quoteMulti: "> para one\n>\n> para two",
	quoteFormatted: "> **bold** in quote and `code`",
	quoteLong: "> " + "word ".repeat(30),
	hr: "above\n\n---\n\nbelow",
	hrStar: "***",
	table: "| a | b |\n|---|---|\n| 1 | 2 |\n| 3 | 4 |",
	tableAlign: "| left | center | right |\n|:-----|:------:|------:|\n| l | c | r |",
	tableWide:
		"| name | description |\n|---|---|\n| alpha | a fairly long description that needs to wrap inside the cell |\n| b | short |",
	tableInline: "| **bold** | `code` |\n|---|---|\n| [l](https://x.y) | *em* |",
	tableThenText: "| a |\n|---|\n| 1 |\nafter",
	htmlBlock: "<div>\nhtml block\n</div>\n\nafter",
	htmlInline: "text with <b>inline</b> html",
	escapes: "\\*not em\\* and \\`not code\\` and \\\\",
	hardBreak: "line one  \nline two\\\nline three",
	tabs: "a\tb\n\n```\n\tindented\n```",
	cjk: "这是一段很长的中文文本，用来测试在窄宽度下的换行是否和派的实现一致，包括标点符号。",
	mixedCjk: "English 和中文 mixed 在一起 with **粗体** text",
	emoji: "Emoji 😀 and ✅ in text 👍🏽 done",
	longWord: "a supercalifragilisticexpialidociouswordthatisverylongindeed b",
	longUrl: "https://example.com/a/very/long/path/that/goes/on/and/on/forever/and/ever",
	streamBold: "Some **bold that is not closed",
	streamCode: "Some `code not closed",
	streamList: "- item\n- ",
	streamHeading: "# ",
	streamTable: "| a | b |\n|---|",
	dollar: "Costs $5 and $10 today",
	numberedNotList: "2024. A year.",
	entity: "AT&amp;T &copy; &lt;tag&gt;",
	unicodePunct: "Quotes “like this” — and ‘this’…",
	headingTrailingHashes: "## Title ##\n\n### Also #",
	linkTitle: '[text](https://x.y "the title") after',
	referenceLink: "See [the docs][docs] and [docs].\n\n[docs]: https://docs.example.com",
	emphasisEdges: "a*b*c **a**b _a_b x__y__z *not closed",
	codespanSpaces: "`` a `b` c `` and ` x ` and `a\nb`",
	orderedZero: "0. zero\n1. one",
	lazyContinuation: "- item\ncontinues lazily\n- next",
	mixedTightLoose: "- a\n- b\n\n- c",
	quoteCode: "> ```\n> code in quote\n> ```\n> after",
	quoteNestedList: "> 1. one\n>    - sub\n> 2. two",
	tablePipes: "| a \\| b | c |\n|---|---|\n| `x\\|y` | z |",
	tableEmptyCells: "| a | b |\n|---|---|\n|  | 2 |\n| 3 |  |",
	tableManyCols: "| a | b | c | d | e | f | g |\n|---|---|---|---|---|---|---|\n| 1 | 2 | 3 | 4 | 5 | 6 | 7 |",
	htmlComment: "before\n\n<!-- a comment -->\n\nafter",
	spacesLine: "first\n   \nsecond",
	trailingSpacesList: "- a  \n- b  ",
	headingThenList: "# H\n- a\n- b",
	listThenHeading: "- a\n# H",
	codeInList: "1. step\n   ```sh\n   run\n   ```\n2. next",
	deepNest: "- a\n  - b\n    - c\n      - d\n        - e",
	longListItemCode: "- `" + "x".repeat(50) + "`",
	streamLink: "see [the link](https://exa",
	streamLinkText: "see [the li",
	streamCodespan: "run `npm ins",
	streamFenceOpen: "```",
	streamFenceLang: "```py",
	streamNestedFence: "- item\n  ```\n  code\n  ``",
	streamQuoteFence: "> ```\n> x\n> ``",
	streamEmphasis: "*ital",
	streamStrike: "~~stri",
	streamTableRow: "| a | b |\n|---|---|\n| 1 |",
	streamOrdered: "1. one\n2.",
	streamHr: "text\n\n--",
	unicodeHeading: "## 你好世界 🌍",
	cjkList: "- 第一项内容比较长需要换行显示出来\n- 第二项",
	cjkTable: "| 名称 | 说明 |\n|---|---|\n| 测试 | 这是一个很长的中文说明文字 |",
	emojiWide: "👨‍👩‍👧‍👦 family and 🇨🇳 flag",
	backslashEnd: "ends with backslash\\",
	numbersDots: "Version 1.2.3. Then 4.",
	asterisksAlone: "* * *\n\nand - - -",
	inlineHtmlEntityMix: "a &lt;b&gt; <i>c</i> &amp;",
	pathsUnderscore: "file_name_here.go and __init__.py",
	urlParens: "https://en.wikipedia.org/wiki/Go_(programming_language) end",
	emailBare: "write to me@example.com today",
	mixed:
		"# Plan\n\nWe will:\n\n1. Read `main.go`\n2. Fix the **bug**\n\n```diff\n- old\n+ new\n```\n\n> Note: careful.\n\nDone.",
};
const widths = [80, 40, 20, 10];

const cases: object[] = [];
for (const hyperlinks of [false, true]) {
	setCapabilities({ images: null, trueColor: true, hyperlinks });
	for (const [name, text] of Object.entries(texts)) {
		for (const width of widths) {
			for (const [style, defaultTextStyle] of Object.entries(styles)) {
				if (style !== "none" && width !== 40) continue;
				if (hyperlinks && !/link|url|Url|autolink|mailto|mixed|image/i.test(name)) continue;
				const paddingX = style === "user" ? 1 : width === 10 ? 0 : 1;
				const paddingY = style === "user" ? 1 : 0;
				const md = new Markdown(text, paddingX, paddingY, theme, defaultTextStyle, { renderLatex: false });
				cases.push({ name, text, width, paddingX, paddingY, style, hyperlinks, lines: md.render(width) });
			}
		}
	}
}

const out = join(dirname(fileURLToPath(import.meta.url)), "golden.json");
writeFileSync(out, `${JSON.stringify(cases, null, "\t")}\n`);
console.log(`wrote ${cases.length} cases to ${out}`);
