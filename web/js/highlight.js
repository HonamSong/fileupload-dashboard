// Tiny self-contained syntax highlighter (no external deps).
// Tokenizes with ordered sticky-regex rules per language and wraps matches in
// <span class="tok-*">. Everything else is HTML-escaped. Falls back to plain
// text for unknown types or very large files.

const HL_MAX = 600 * 1024; // don't highlight beyond this many chars (perf)

// Map file extension / filename -> language id.
function hlLangFor(name) {
  const n = (name || "").toLowerCase();
  const base = n.split("/").pop();
  if (["dockerfile"].includes(base) || base.startsWith("dockerfile")) return "shell";
  if (base === "makefile") return "shell";
  const ext = base.includes(".") ? base.split(".").pop() : "";
  const map = {
    json: "json", js: "clike", mjs: "clike", ts: "clike", go: "clike",
    c: "clike", h: "clike", cc: "clike", cpp: "clike", java: "clike", rs: "clike",
    sh: "shell", bash: "shell", zsh: "shell", env: "ini", conf: "ini", cfg: "ini",
    ini: "ini", toml: "ini", properties: "ini",
    yaml: "yaml", yml: "yaml",
    py: "python",
    sql: "sql",
    md: "markdown", markdown: "markdown",
    html: "markup", htm: "markup", xml: "markup", svg: "markup",
  };
  return map[ext] || "";
}

// Rule sets. Each rule: {cls, re} with the sticky (y) flag. Order matters —
// comments and strings must come before keywords/numbers.
const HL_KW_CLIKE = "break case catch class const continue default defer delete do else enum export extends false finally for func function go goto if import in interface let map new nil null package range return struct switch this true try type var void while yield await async new".split(" ");
const HL_KW_PY = "and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield True False None self".split(" ");
const HL_KW_SH = "if then else elif fi for while until do done case esac function in select return export local readonly declare echo set unset source alias".split(" ");
const HL_KW_SQL = "select from where insert update delete into values create table drop alter add primary key foreign references index join left right inner outer on group by order having limit offset union all as and or not null distinct set default".split(" ");

function hlWord(list) { return new RegExp("\\b(?:" + list.join("|") + ")\\b", "y"); }

function hlRules(lang) {
  const strD = { cls: "str", re: /"(?:\\.|[^"\\])*"/y };
  const strS = { cls: "str", re: /'(?:\\.|[^'\\])*'/y };
  const tmpl = { cls: "str", re: /`(?:\\.|[^`\\])*`/y };
  const num = { cls: "num", re: /-?\b\d[\d_]*(?:\.\d+)?(?:[eE][+-]?\d+)?\b/y };
  const boolNull = { cls: "bool", re: /\b(?:true|false|null|nil|True|False|None)\b/y };
  switch (lang) {
    case "json":
      return [
        { cls: "key", re: /"(?:\\.|[^"\\])*"(?=\s*:)/y },
        strD, num, boolNull,
        { cls: "punct", re: /[{}\[\]:,]/y },
      ];
    case "clike":
      return [
        { cls: "comment", re: /\/\/[^\n]*/y },
        { cls: "comment", re: /\/\*[\s\S]*?\*\//y },
        strD, strS, tmpl, boolNull, num,
        { cls: "kw", re: hlWord(HL_KW_CLIKE) },
      ];
    case "shell":
      return [
        { cls: "comment", re: /#[^\n]*/y },
        strD, strS,
        { cls: "var", re: /\$\{[^}]*\}|\$\w+/y },
        { cls: "kw", re: hlWord(HL_KW_SH) },
        num,
      ];
    case "python":
      return [
        { cls: "comment", re: /#[^\n]*/y },
        { cls: "str", re: /"""[\s\S]*?"""|'''[\s\S]*?'''/y },
        strD, strS, boolNull, num,
        { cls: "kw", re: hlWord(HL_KW_PY) },
      ];
    case "sql":
      return [
        { cls: "comment", re: /--[^\n]*/y },
        strS, strD, num,
        { cls: "kw", re: new RegExp("\\b(?:" + HL_KW_SQL.join("|") + ")\\b", "iy") },
      ];
    case "ini":
      return [
        { cls: "comment", re: /[#;][^\n]*/y },
        { cls: "key", re: /^[ \t]*[\w.-]+(?=\s*=)/my },
        { cls: "str", re: /"(?:\\.|[^"\\])*"|'(?:[^'\\])*'/y },
        boolNull, num,
      ];
    case "yaml":
      return [
        { cls: "comment", re: /#[^\n]*/y },
        { cls: "key", re: /^[ \t-]*[\w.-]+(?=\s*:)/my },
        strD, strS, boolNull, num,
      ];
    case "markdown":
      return [
        { cls: "kw", re: /^#{1,6} [^\n]*/my },        // headings
        { cls: "str", re: /`[^`\n]*`|```[\s\S]*?```/y }, // code
        { cls: "bool", re: /\*\*[^*\n]+\*\*|_[^_\n]+_/y }, // bold/italic
        { cls: "var", re: /\[[^\]\n]*\]\([^)\n]*\)/y }, // links
      ];
    case "markup":
      return [
        { cls: "comment", re: /<!--[\s\S]*?-->/y },
        { cls: "kw", re: /<\/?[a-zA-Z][\w:-]*/y },
        { cls: "key", re: /[\w:-]+(?==)/y },
        strD, strS,
        { cls: "punct", re: /[<>\/]/y },
      ];
    default:
      return null;
  }
}

// Highlight code into HTML. Returns null if unsupported/too large (caller should
// fall back to plain text).
function highlightCode(code, name) {
  const lang = hlLangFor(name);
  if (!lang || code.length > HL_MAX) return null;
  const rules = hlRules(lang);
  if (!rules) return null;
  let out = "", i = 0;
  const n = code.length;
  while (i < n) {
    let matched = false;
    for (const r of rules) {
      r.re.lastIndex = i;
      const m = r.re.exec(code);
      if (m && m[0].length > 0) {
        out += `<span class="tok-${r.cls}">${esc(m[0])}</span>`;
        i += m[0].length;
        matched = true;
        break;
      }
    }
    if (!matched) { out += esc(code[i]); i++; }
  }
  return out;
}
