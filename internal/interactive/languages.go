package interactive

import "strings"

// highlightLanguages are the language names and aliases highlight.js 10.7.3
// — the version Pi highlights code with — knows. Pi draws code in a
// language it knows with that language's highlighting, and other code in
// one colour.
var highlightLanguages = map[string]bool{
	"1c": true, "abnf": true, "accesslog": true, "actionscript": true, "ada": true, "ado": true,
	"adoc": true, "ahk": true, "angelscript": true, "apache": true, "apacheconf": true,
	"applescript": true, "arcade": true, "arduino": true, "arm": true, "armasm": true, "as": true,
	"asc": true, "asciidoc": true, "aspectj": true, "atom": true, "autohotkey": true, "autoit": true,
	"avrasm": true, "awk": true, "axapta": true, "bash": true, "basic": true, "bat": true, "bf": true,
	"bind": true, "bnf": true, "brainfuck": true, "c": true, "c#": true, "c++": true, "c-like": true,
	"cal": true, "capnp": true, "capnproto": true, "cc": true, "ceylon": true, "cjs": true,
	"clean": true, "clj": true, "clojure": true, "clojure-repl": true, "cls": true, "cmake": true,
	"cmake.in": true, "cmd": true, "coffee": true, "coffeescript": true, "console": true, "coq": true,
	"cos": true, "cpp": true, "cr": true, "craftcms": true, "crm": true, "crmsh": true,
	"crystal": true, "cs": true, "csharp": true, "cson": true, "csp": true, "css": true, "cxx": true,
	"d": true, "dart": true, "dcl": true, "delphi": true, "dfm": true, "diff": true, "django": true,
	"dns": true, "do": true, "docker": true, "dockerfile": true, "dos": true, "dpr": true,
	"dsconfig": true, "dst": true, "dts": true, "dust": true, "ebnf": true, "elixir": true,
	"elm": true, "erb": true, "erl": true, "erlang": true, "erlang-repl": true, "excel": true,
	"f90": true, "f95": true, "feature": true, "fix": true, "flix": true, "fortran": true,
	"freepascal": true, "fs": true, "fsharp": true, "gams": true, "gauss": true, "gcode": true,
	"gemspec": true, "gherkin": true, "glsl": true, "gml": true, "gms": true, "go": true,
	"golang": true, "golo": true, "gradle": true, "graph": true, "groovy": true, "gss": true,
	"gyp": true, "h": true, "h++": true, "haml": true, "handlebars": true, "haskell": true,
	"haxe": true, "hbs": true, "hh": true, "hpp": true, "hs": true, "hsp": true, "html": true,
	"html.handlebars": true, "html.hbs": true, "htmlbars": true, "http": true, "https": true,
	"hx": true, "hxx": true, "hy": true, "hylang": true, "i7": true, "iced": true, "icl": true,
	"inform7": true, "ini": true, "ino": true, "instances": true, "ipython": true, "irb": true,
	"irpf90": true, "isbl": true, "java": true, "javascript": true, "jboss-cli": true, "jinja": true,
	"js": true, "json": true, "jsp": true, "jsx": true, "julia": true, "julia-repl": true, "k": true,
	"kdb": true, "kotlin": true, "kt": true, "kts": true, "lasso": true, "lassoscript": true,
	"latex": true, "lazarus": true, "ldif": true, "leaf": true, "less": true, "lfm": true,
	"lisp": true, "livecodeserver": true, "livescript": true, "llvm": true, "lpr": true, "ls": true,
	"lsl": true, "lua": true, "m": true, "mak": true, "make": true, "makefile": true,
	"markdown": true, "mathematica": true, "matlab": true, "maxima": true, "md": true, "mel": true,
	"mercury": true, "mikrotik": true, "mips": true, "mipsasm": true, "mizar": true, "mjs": true,
	"mk": true, "mkd": true, "mkdown": true, "ml": true, "mm": true, "mma": true, "mojolicious": true,
	"monkey": true, "moo": true, "moon": true, "moonscript": true, "mysql": true, "n1ql": true,
	"nc": true, "nginx": true, "nginxconf": true, "nim": true, "nix": true, "nixos": true,
	"node-repl": true, "nsis": true, "obj-c": true, "obj-c++": true, "objc": true,
	"objective-c++": true, "objectivec": true, "ocaml": true, "openscad": true, "oracle": true,
	"osascript": true, "oxygene": true, "p21": true, "parser3": true, "pas": true, "pascal": true,
	"patch": true, "pb": true, "pbi": true, "pcmk": true, "perl": true, "pf": true, "pf.conf": true,
	"pgsql": true, "php": true, "php-template": true, "php3": true, "php4": true, "php5": true,
	"php6": true, "php7": true, "php8": true, "pl": true, "plaintext": true, "plist": true,
	"pm": true, "podspec": true, "pony": true, "postgres": true, "postgresql": true,
	"powershell": true, "pp": true, "processing": true, "profile": true, "prolog": true,
	"properties": true, "protobuf": true, "ps": true, "ps1": true, "puppet": true, "purebasic": true,
	"py": true, "pycon": true, "python": true, "python-repl": true, "q": true, "qml": true,
	"qt": true, "r": true, "rb": true, "re": true, "reasonml": true, "rib": true, "roboconf": true,
	"routeros": true, "rs": true, "rsl": true, "rss": true, "ruby": true, "ruleslanguage": true,
	"rust": true, "sas": true, "scad": true, "scala": true, "scheme": true, "sci": true,
	"scilab": true, "scss": true, "sh": true, "shell": true, "smali": true, "smalltalk": true,
	"sml": true, "sqf": true, "sql": true, "sql_more": true, "st": true, "stan": true,
	"stanfuncs": true, "stata": true, "step": true, "step21": true, "stp": true, "styl": true,
	"stylus": true, "subunit": true, "sv": true, "svg": true, "svh": true, "swift": true,
	"taggerscript": true, "tao": true, "tap": true, "tcl": true, "tex": true, "text": true,
	"thor": true, "thrift": true, "tk": true, "toml": true, "tp": true, "ts": true, "tsx": true,
	"twig": true, "txt": true, "typescript": true, "v": true, "vala": true, "vb": true, "vbnet": true,
	"vbs": true, "vbscript": true, "vbscript-html": true, "verilog": true, "vhdl": true, "vim": true,
	"wildfly-cli": true, "wl": true, "wsf": true, "x++": true, "x86asm": true, "xhtml": true,
	"xjb": true, "xl": true, "xls": true, "xlsx": true, "xml": true, "xpath": true, "xq": true,
	"xquery": true, "xsd": true, "xsl": true, "yaml": true, "yml": true, "zep": true, "zephir": true,
	"zone": true, "zsh": true,
}

// knownLanguage reports a language Pi would highlight.
func knownLanguage(lang string) bool { return lang != "" && highlightLanguages[strings.ToLower(lang)] }

// extensionLanguages map file extensions to the language Pi highlights
// them as (getLanguageFromPath).
var extensionLanguages = map[string]string{
	"ts": "typescript", "tsx": "typescript", "js": "javascript", "jsx": "javascript", "mjs": "javascript",
	"cjs": "javascript", "py": "python", "rb": "ruby", "rs": "rust", "go": "go", "java": "java", "kt": "kotlin",
	"swift": "swift", "c": "c", "h": "c", "cpp": "cpp", "cc": "cpp", "cxx": "cpp", "hpp": "cpp", "cs": "csharp",
	"php": "php", "sh": "bash", "bash": "bash", "zsh": "bash", "fish": "fish", "ps1": "powershell", "sql": "sql",
	"html": "html", "htm": "html", "css": "css", "scss": "scss", "sass": "sass", "less": "less", "json": "json",
	"yaml": "yaml", "yml": "yaml", "toml": "toml", "xml": "xml", "md": "markdown", "markdown": "markdown",
	"dockerfile": "dockerfile", "makefile": "makefile", "cmake": "cmake", "lua": "lua", "perl": "perl", "r": "r",
	"scala": "scala", "clj": "clojure", "ex": "elixir", "exs": "elixir", "erl": "erlang", "hs": "haskell",
	"ml": "ocaml", "vim": "vim", "graphql": "graphql", "proto": "protobuf", "tf": "hcl", "hcl": "hcl",
}

// languageFromPath is the language a file's extension names, or "".
func languageFromPath(path string) string {
	return extensionLanguages[strings.ToLower(path[strings.LastIndexByte(path, '.')+1:])]
}
