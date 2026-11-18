#!/usr/bin/env node
/**
 * 用户可见消息键守护（preflight-keys-ui）。
 *
 * 只检查真正会进入 UI 文案的数据路径，不把领域事件名或日志标签当缺失翻译：
 * - app-server 生产 Go 代码中的 ErrorInfo/Error/PreflightIssue/EnvironmentCheck
 *   构造（`MessageKey:` 字段、`messageKey :=/=` 赋值、`errConflictf` 与
 *   `failInstallation` 的 messageKey 参数、`newCheck` 的环境检查键）；
 * - 有限动态键：六槽位 ReasonCode → `preflight.<code>`，以及显式列出的
 *   mock 演示键；
 * - UI 中的静态 `t("...")` / `t('...')` 引用、`messageKey` 字面量与
 *   `errorText(..., "fallback")`；
 * - 复数 helper `tCount("key", count)` / `countNotice("key", count)` 展开为
 *   `key.one` + `key.other`；
 * - 响应式提示 helper `messageNotice("key")` / `errorNotice(err, "prefix")` /
 *   `fallbackErrorNotice(err, "fallback")` / `countNotice("key")` /
 *   `noticeKey("key")` 的字面量键；
 *   键实参必须是完整字符串字面量：闭引号后只能跟注释/空白，随后是逗号或
 *   调用结束。字符串拼接、模板插值、变量实参都判为动态键并失败（扫描顶层
 *   实参，不是前缀正则；helper 定义所在 i18n 模块不参与动态检测）；
 * - `ui/src/i18n/errorCodes.json` 的已知错误码 → 文案键映射。
 *
 * 另外校验：
 * - 所有发现的 area messages 文件都经 register.ts 实际注册，避免删除注册后假通过；
 * - 每个键必须同时存在于 en 与 zh-CN，且两边占位参数一致。
 *
 * 用法：
 *   node scripts/check-i18n-keys.mjs
 *   node --test scripts/check-i18n-keys.test.mjs
 */
import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import ts from "typescript";

const defaultRepoRoot = fileURLToPath(new URL("..", import.meta.url));
// 文案键为点分字符串，段内允许 camelCase（如 tools.flow.importEmpty）与 snake_case。
// 首段保持小写起始，减少把 URL、导入路径等无关字面量计为用户文案。
const keyPattern = /^[a-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)+$/;
const placeholderPattern = /\{([A-Za-z0-9_]+)\}/g;

function walk(dir, accept) {
  const out = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      out.push(...walk(full, accept));
    } else if (accept(full)) {
      out.push(full);
    }
  }
  return out;
}

function parseTs(file) {
  const text = fs.readFileSync(file, "utf8");
  return ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
}

/** 解析一个 AreaMessages 文件里的 en / zhCN 两个对象。 */
function parseAreaFile(file, repoRoot) {
  const text = fs.readFileSync(file, "utf8");
  const enStart = text.indexOf("en: {");
  const zhStart = text.indexOf("zhCN: {");
  if (enStart < 0 || zhStart < 0 || zhStart <= enStart) {
    throw new Error(`无法识别 en/zhCN 结构: ${path.relative(repoRoot, file)}`);
  }
  return {
    en: extractEntries(text.slice(enStart, zhStart)),
    zh: extractEntries(text.slice(zhStart)),
  };
}

function extractEntries(region) {
  const entries = new Map();
  const lines = region.split("\n");
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index].trim();
    if (!line.startsWith('"')) continue;
    const match = /^"([^"]+)"\s*:\s*(.*)$/.exec(line);
    if (!match) continue;
    let rest = match[2].trim();
    if (!rest.startsWith('"')) {
      // 值写在下一行，例如 `"access.setupHint":\n  "..."`。
      for (let next = index + 1; next < lines.length; next += 1) {
        const candidate = lines[next].trim();
        if (!candidate || candidate.startsWith("//")) continue;
        rest = candidate;
        break;
      }
    }
    const valueMatch = /^"((?:[^"\\]|\\.)*)"/.exec(rest);
    entries.set(match[1], valueMatch ? valueMatch[1] : "");
  }
  return entries;
}

function findAreaFiles(uiRoot) {
  const files = [
    path.join(uiRoot, "i18n", "base.ts"),
    ...walk(
      uiRoot,
      (file) => file.endsWith("messages.ts") || file.endsWith("cardMessages.ts"),
    ),
  ];
  if (files.length < 2) {
    throw new Error("未找到 i18n 文案区域文件");
  }
  return files;
}

function resolveModuleFile(fromFile, specifier, uiRoot) {
  let base;
  if (specifier.startsWith("@/")) {
    base = path.join(uiRoot, specifier.slice(2));
  } else if (specifier.startsWith(".")) {
    base = path.resolve(path.dirname(fromFile), specifier);
  } else {
    return undefined;
  }
  const candidates = [
    base,
    `${base}.ts`,
    `${base}.tsx`,
    `${base}.js`,
    path.join(base, "index.ts"),
  ];
  return candidates.find(
    (candidate) => fs.existsSync(candidate) && fs.statSync(candidate).isFile(),
  );
}

/** register.ts 中 import 的本地标识符 → 实际文件。 */
function importBindings(registerFile, uiRoot) {
  const source = parseTs(registerFile);
  const bindings = new Map();
  for (const statement of source.statements) {
    if (!ts.isImportDeclaration(statement) || !statement.importClause) continue;
    const specifier = statement.moduleSpecifier;
    if (!ts.isStringLiteral(specifier)) continue;
    const resolved = resolveModuleFile(registerFile, specifier.text, uiRoot);
    if (!resolved) continue;
    const clause = statement.importClause;
    if (clause.name) bindings.set(clause.name.text, resolved);
    if (clause.namedBindings && ts.isNamedImports(clause.namedBindings)) {
      for (const element of clause.namedBindings.elements) {
        bindings.set(element.name.text, resolved);
      }
    }
  }
  return bindings;
}

/**
 * 收集 register.ts 实际传给 registerMessages 的标识符：
 * - 直接调用 registerMessages(identifier)；
 * - `const areas = [...]; for (const area of areas) registerMessages(area)`。
 */
function collectRegisteredIdentifiers(sourceFile) {
  const names = new Set();

  const visitCalls = (node) => {
    if (
      ts.isCallExpression(node) &&
      ts.isIdentifier(node.expression) &&
      node.expression.text === "registerMessages"
    ) {
      for (const argument of node.arguments) {
        if (ts.isIdentifier(argument)) names.add(argument.text);
      }
    }
    ts.forEachChild(node, visitCalls);
  };
  visitCalls(sourceFile);

  const findArrayInitializer = (name) => {
    let found;
    const visit = (node) => {
      if (
        ts.isVariableDeclaration(node) &&
        ts.isIdentifier(node.name) &&
        node.name.text === name &&
        node.initializer &&
        ts.isArrayLiteralExpression(node.initializer)
      ) {
        found = node.initializer;
      }
      ts.forEachChild(node, visit);
    };
    visit(sourceFile);
    return found;
  };

  const visitLoops = (node) => {
    if (ts.isForOfStatement(node) && ts.isIdentifier(node.expression)) {
      const declarations = ts.isVariableDeclarationList(node.initializer)
        ? node.initializer.declarations
        : [];
      const loopName =
        declarations.length === 1 && ts.isIdentifier(declarations[0].name)
          ? declarations[0].name.text
          : undefined;
      if (loopName) {
        let registersLoop = false;
        const scan = (child) => {
          if (
            ts.isCallExpression(child) &&
            ts.isIdentifier(child.expression) &&
            child.expression.text === "registerMessages" &&
            child.arguments.some(
              (argument) => ts.isIdentifier(argument) && argument.text === loopName,
            )
          ) {
            registersLoop = true;
          }
          ts.forEachChild(child, scan);
        };
        scan(node.statement);
        if (registersLoop) {
          const array = findArrayInitializer(node.expression.text);
          if (array) {
            for (const element of array.elements) {
              if (ts.isIdentifier(element)) names.add(element.text);
            }
          }
        }
      }
    }
    ts.forEachChild(node, visitLoops);
  };
  visitLoops(sourceFile);

  return names;
}

/** register.ts 实际注册的 area 文件集合。 */
function registeredAreaFiles(uiRoot) {
  const registerFile = path.join(uiRoot, "i18n", "register.ts");
  if (!fs.existsSync(registerFile)) {
    throw new Error("未找到 ui/src/i18n/register.ts，无法校验文案区域注册");
  }
  const bindings = importBindings(registerFile, uiRoot);
  const registered = new Set();
  for (const name of collectRegisteredIdentifiers(parseTs(registerFile))) {
    const file = bindings.get(name);
    if (file) registered.add(file);
  }
  return registered;
}

function collectRegisteredMessages(repoRoot, uiRoot) {
  const discovered = findAreaFiles(uiRoot);
  const registered = registeredAreaFiles(uiRoot);
  const unregisteredAreaFiles = discovered.filter((file) => !registered.has(file));
  const en = new Map();
  const zh = new Map();
  // 未注册的文件不进入运行时字典，不能拿它冒充已注册文案。
  for (const file of discovered) {
    if (!registered.has(file)) continue;
    const parsed = parseAreaFile(file, repoRoot);
    for (const [key, value] of parsed.en) en.set(key, value);
    for (const [key, value] of parsed.zh) zh.set(key, value);
  }
  return { en, zh, unregisteredAreaFiles };
}

function collectGoKeys(repoRoot, goRoot) {
  const keys = new Map();
  const add = (key, source) => {
    if (!keyPattern.test(key)) return;
    if (!keys.has(key)) keys.set(key, new Set());
    keys.get(key).add(source);
  };
  const files = walk(goRoot, (file) => file.endsWith(".go") && !file.endsWith("_test.go"));
  const textByFile = new Map();
  for (const file of files) {
    const text = fs.readFileSync(file, "utf8");
    textByFile.set(file, text);
    const source = path.relative(repoRoot, file).split(path.sep).join("/");
    for (const match of text.matchAll(/MessageKey:\s*"([^"]+)"/g)) add(match[1], source);
    for (const match of text.matchAll(/\bmessageKey\s*:?=\s*"([^"]+)"/g)) add(match[1], source);
    for (const match of text.matchAll(/failInstallation\s*\(([\s\S]*?)\)/g)) {
      for (const literal of match[1].matchAll(/"([^"]+)"/g)) add(literal[1], source);
    }
    for (const match of text.matchAll(/errConflictf\s*\(([\s\S]*?)\)/g)) {
      for (const literal of match[1].matchAll(/"([^"]+)"/g)) add(literal[1], source);
    }
    for (const match of text.matchAll(/newCheck\s*\(([\s\S]*?)\)/g)) {
      for (const literal of match[1].matchAll(/"((?:environment\.)[^"]+)"/g)) add(literal[1], source);
    }
  }

  // 有限动态键：runs.go 把能力的 ReasonCode 转成 preflight.<lower_snake>。
  const domainTypes = textByFile.get(path.join(goRoot, "internal", "domain", "types.go")) ?? "";
  const errorCodes = new Map();
  for (const match of domainTypes.matchAll(/ErrorCode(\w+)\s+ErrorCode\s*=\s*"([^"]+)"/g)) {
    errorCodes.set(match[1], match[2]);
  }
  const capabilities =
    textByFile.get(path.join(goRoot, "internal", "service", "capabilities.go")) ?? "";
  for (const match of capabilities.matchAll(/ReasonCode\s*=\s*string\(domain\.ErrorCode(\w+)\)/g)) {
    const code = errorCodes.get(match[1]);
    if (!code) {
      throw new Error(`无法解析 ReasonCode 错误码常量: ${match[1]}`);
    }
    add(
      `preflight.${code.toLowerCase().replaceAll("-", "_")}`,
      "app-server ReasonCode 动态键",
    );
  }
  return keys;
}

// ---- 新 helper 的实参扫描 ----
// 字面量键必须是完整实参：闭引号后只能是注释/空白，随后是逗号或调用结束。
// 拼接（"a" + b）、模板插值（`a${b}`）、变量实参都会判为动态键。
// 不依赖单一前缀正则，改为逐字符扫描顶层实参（处理字符串/注释/括号/多行）。
const HELPER_KEY_ARG_INDEX = new Map([
  ["tCount", 0],
  ["countNotice", 0],
  ["messageNotice", 0],
  ["noticeKey", 0],
  ["errorNotice", 1],
  ["fallbackErrorNotice", 1],
]);
const PLURAL_HELPERS = new Set(["tCount", "countNotice"]);
const HELPER_CALL_PATTERN =
  /\b(tCount|countNotice|messageNotice|noticeKey|errorNotice|fallbackErrorNotice)\s*\(/g;

function skipSourceTrivia(text, index) {
  let i = index;
  for (;;) {
    while (i < text.length && /\s/.test(text[i])) i += 1;
    if (text.startsWith("//", i)) {
      const end = text.indexOf("\n", i);
      if (end === -1) return text.length;
      i = end + 1;
      continue;
    }
    if (text.startsWith("/*", i)) {
      const end = text.indexOf("*/", i + 2);
      if (end === -1) return text.length;
      i = end + 2;
      continue;
    }
    return i;
  }
}

/** 跳过一个字符串/模板字面量，处理转义与模板插值嵌套；返回结束下标。 */
function skipStringLiteral(text, index) {
  const quote = text[index];
  let i = index + 1;
  while (i < text.length) {
    const ch = text[i];
    if (ch === "\\") {
      i += 2;
      continue;
    }
    if (quote === "`" && ch === "$" && text[i + 1] === "{") {
      i += 2;
      let depth = 1;
      while (i < text.length && depth > 0) {
        const inner = text[i];
        if (inner === "\\") {
          i += 2;
          continue;
        }
        if (inner === '"' || inner === "'" || inner === "`") {
          i = skipStringLiteral(text, i);
          continue;
        }
        if (inner === "{") depth += 1;
        else if (inner === "}") depth -= 1;
        i += 1;
      }
      continue;
    }
    if (ch === quote) return i + 1;
    i += 1;
  }
  return text.length;
}

/** 仅接受完整、无转义/无插值的字符串字面量；其余按动态处理（失败关闭）。 */
function readStaticStringLiteral(text, index) {
  const quote = text[index];
  if (quote !== '"' && quote !== "'" && quote !== "`") return null;
  let i = index + 1;
  let value = "";
  while (i < text.length) {
    const ch = text[i];
    if (ch === "\\") return null;
    if (quote === "`" && ch === "$" && text[i + 1] === "{") return null;
    if (ch === quote) return { value, end: i + 1 };
    if (ch === "\n" && quote !== "`") return null;
    value += ch;
    i += 1;
  }
  return null;
}

/** 扫描 `(` 起的顶层实参，返回实参原文与结束下标。 */
function splitHelperArguments(text, openIndex) {
  const args = [];
  let i = openIndex + 1;
  let start = i;
  let depth = 0;
  while (i < text.length) {
    i = skipSourceTrivia(text, i);
    if (i >= text.length) return null;
    const ch = text[i];
    if (ch === '"' || ch === "'" || ch === "`") {
      i = skipStringLiteral(text, i);
      continue;
    }
    if (ch === "(" || ch === "[" || ch === "{") {
      depth += 1;
      i += 1;
      continue;
    }
    if (ch === ")" || ch === "]" || ch === "}") {
      if (ch === ")" && depth === 0) {
        args.push(text.slice(start, i));
        return { args, end: i + 1 };
      }
      depth = Math.max(0, depth - 1);
      i += 1;
      continue;
    }
    if (ch === "," && depth === 0) {
      args.push(text.slice(start, i));
      start = i + 1;
      i += 1;
      continue;
    }
    i += 1;
  }
  return null;
}

/** 实参只允许「注释/空白 + 完整字符串字面量 + 注释/空白」。 */
function classifyHelperArgument(raw) {
  const start = skipSourceTrivia(raw, 0);
  if (start >= raw.length) return { kind: "missing" };
  const literal = readStaticStringLiteral(raw, start);
  if (!literal) return { kind: "dynamic", snippet: raw.slice(start).trim() };
  const end = skipSourceTrivia(raw, literal.end);
  if (end !== raw.length) return { kind: "dynamic", snippet: raw.slice(start).trim() };
  return { kind: "literal", value: literal.value };
}

function scanHelperCalls(text, source, detectDynamic) {
  const literals = [];
  const dynamicKeys = [];
  HELPER_CALL_PATTERN.lastIndex = 0;
  let match;
  while ((match = HELPER_CALL_PATTERN.exec(text)) !== null) {
    const name = match[1];
    const prefix = text.slice(Math.max(0, match.index - 20), match.index);
    if (/(?:^|[^\w$])function\s*$/.test(prefix)) continue;
    const parsed = splitHelperArguments(text, match.index + match[0].length - 1);
    if (!parsed) continue;
    const argIndex = HELPER_KEY_ARG_INDEX.get(name);
    const raw = argIndex === undefined ? undefined : parsed.args[argIndex];
    if (raw === undefined) continue;
    const classified = classifyHelperArgument(raw);
    if (classified.kind === "missing") continue;
    if (classified.kind === "literal") {
      if (PLURAL_HELPERS.has(name)) {
        literals.push(`${classified.value}.one`, `${classified.value}.other`);
      } else {
        literals.push(classified.value);
      }
      continue;
    }
    if (!detectDynamic) continue;
    const label =
      argIndex === 1
        ? `${name}(..., ${classified.snippet}`
        : `${name}(${classified.snippet}`;
    dynamicKeys.push(`${source}: ${label}`);
  }
  return { literals, dynamicKeys };
}

function collectUiKeys(repoRoot, uiRoot) {
  const keys = new Map();
  const dynamicKeys = [];
  const add = (key, source) => {
    if (!keyPattern.test(key)) return;
    if (!keys.has(key)) keys.set(key, new Set());
    keys.get(key).add(source);
  };
  const files = walk(uiRoot, (file) => file.endsWith(".ts") || file.endsWith(".vue"));
  for (const file of files) {
    const text = fs.readFileSync(file, "utf8");
    const source = path.relative(repoRoot, file).split(path.sep).join("/");
    for (const match of text.matchAll(/\bt\(\s*(?:"([^"]+)"|'([^']+)')/g)) {
      add(match[1] ?? match[2], source);
    }
    for (const match of text.matchAll(/\bmessageKey\s*:\s*(?:"([^"]+)"|'([^']+)')/g)) {
      add(match[1] ?? match[2], source);
    }
    for (const match of text.matchAll(
      /\b(?:errorText|displayErrorText)\(\s*[^,\n]+,\s*(?:"([^"]+)"|'([^']+)')/g,
    )) {
      add(match[1] ?? match[2], source);
    }
    // 数量/提示 helper 的键实参必须是完整字面量；.one/.other 一并守护。
    const scanned = scanHelperCalls(text, source, !source.startsWith("ui/src/i18n/"));
    for (const key of scanned.literals) add(key, source);
    dynamicKeys.push(...scanned.dynamicKeys);
  }
  return { keys, dynamicKeys };
}

function collectErrorCodeMapping(repoRoot, uiRoot) {
  const mappingFile = path.join(uiRoot, "i18n", "errorCodes.json");
  const mapping = JSON.parse(fs.readFileSync(mappingFile, "utf8"));
  const keys = new Map();
  for (const [code, key] of Object.entries(mapping)) {
    if (!/^[A-Z][A-Z0-9_]*$/.test(code)) {
      throw new Error(`errorCodes.json 错误码不是大写常量: ${code}`);
    }
    if (!keyPattern.test(key)) {
      throw new Error(`errorCodes.json 文案键不合法: ${code} -> ${key}`);
    }
    if (!keys.has(key)) keys.set(key, new Set());
    keys.get(key).add(path.relative(repoRoot, mappingFile));
  }
  return keys;
}

function placeholders(value) {
  return [...value.matchAll(placeholderPattern)]
    .map((match) => match[1])
    .sort()
    .join(",");
}

export function runGuard({ repoRoot = defaultRepoRoot } = {}) {
  const goRoot = path.join(repoRoot, "app-server");
  const uiRoot = path.join(repoRoot, "ui", "src");
  if (!fs.existsSync(uiRoot)) {
    throw new Error(`未找到 UI 源码目录: ${uiRoot}`);
  }
  const { en, zh, unregisteredAreaFiles } = collectRegisteredMessages(repoRoot, uiRoot);
  const uiKeys = collectUiKeys(repoRoot, uiRoot);
  const userKeys = new Map();
  for (const source of [
    collectGoKeys(repoRoot, goRoot),
    uiKeys.keys,
    collectErrorCodeMapping(repoRoot, uiRoot),
  ]) {
    for (const [key, origins] of source) {
      if (!userKeys.has(key)) userKeys.set(key, new Set());
      for (const origin of origins) userKeys.get(key).add(origin);
    }
  }

  const missingEn = [];
  const missingZh = [];
  for (const [key, origins] of [...userKeys].sort(([a], [b]) => a.localeCompare(b))) {
    if (!en.has(key)) missingEn.push(`${key}  (${[...origins].join(", ")})`);
    if (!zh.has(key)) missingZh.push(`${key}  (${[...origins].join(", ")})`);
  }

  const placeholderMismatches = [];
  for (const [key, value] of [...en].sort(([a], [b]) => a.localeCompare(b))) {
    const zhValue = zh.get(key);
    if (zhValue === undefined || !value || !zhValue) continue;
    const expected = placeholders(value);
    const actual = placeholders(zhValue);
    if (expected !== actual) {
      placeholderMismatches.push(`${key}: en={${expected}} zh-CN={${actual}}`);
    }
  }

  return {
    ok:
      unregisteredAreaFiles.length === 0 &&
      missingEn.length === 0 &&
      missingZh.length === 0 &&
      placeholderMismatches.length === 0 &&
      uiKeys.dynamicKeys.length === 0,
    userKeyCount: userKeys.size,
    missingEn,
    missingZh,
    placeholderMismatches,
    dynamicKeys: uiKeys.dynamicKeys,
    unregisteredAreaFiles: unregisteredAreaFiles.map((file) =>
      path.relative(repoRoot, file),
    ),
  };
}

/**
 * 24 个懒加载语言包（ui/src/i18n/locales/*.json）的 parity 守护：
 * 键集合、键序（register.ts 注册序）与 {placeholder} 必须与当时的英文词典完全一致。
 * 词典并行增删键后，语言包必须重新同步——这里防止漂移静默发生。
 */
function checkLocalePacks(en, repoRoot, uiRoot) {
  const localesDir = path.join(uiRoot, "i18n", "locales");
  const issues = [];
  const enPlaceholders = (value) =>
    new Set([...value.matchAll(/\{([A-Za-z0-9_]+)\}/g)].map((m) => m[1]));

  // 英文键序 = register.ts 注册序（与运行时 registerMessages 赋值语义一致：
  // 首次出现定位置，重复注册覆盖值）。
  const registeredFiles = registeredAreaFiles(uiRoot);
  const orderedEn = [];
  const seen = new Set();
  for (const name of collectRegisteredIdentifiers(parseTs(path.join(uiRoot, "i18n", "register.ts")))) {
    const file = importBindings(path.join(uiRoot, "i18n", "register.ts"), uiRoot).get(name);
    if (!file || !registeredFiles.has(file)) continue;
    const parsed = parseAreaFile(file, repoRoot);
    for (const key of parsed.en.keys()) {
      if (!seen.has(key)) {
        seen.add(key);
        orderedEn.push(key);
      }
    }
  }

  const packs = fs
    .readdirSync(localesDir)
    .filter((file) => file.endsWith(".json"))
    .sort();
  if (packs.length === 0) issues.push("locales/ 目录为空：语言包缺失或未生成");
  for (const file of packs) {
    const code = file.replace(/\.json$/, "");
    let dict;
    try {
      dict = JSON.parse(fs.readFileSync(path.join(localesDir, file), "utf8"));
    } catch (err) {
      issues.push(`${code}: JSON 解析失败 (${err.message})`);
      continue;
    }
    const keys = Object.keys(dict);
    const missing = orderedEn.filter((key) => !(key in dict));
    const extra = keys.filter((key) => !seen.has(key));
    if (missing.length) issues.push(`${code}: 缺 ${missing.length} 键（如 ${missing.slice(0, 3).join(", ")}）`);
    if (extra.length) issues.push(`${code}: 多 ${extra.length} 键（如 ${extra.slice(0, 3).join(", ")}）`);
    // 键序：只比较两边都存在的键的相对顺序。
    let cursor = -1;
    for (const key of keys) {
      const idx = orderedEn.indexOf(key);
      if (idx === -1) continue;
      if (idx < cursor) {
        issues.push(`${code}: 键序与英文词典不一致（首个乱序键 ${key}）`);
        break;
      }
      cursor = idx;
    }
    for (const [key, value] of Object.entries(dict)) {
      if (typeof value !== "string" || !en.has(key)) continue;
      const expected = enPlaceholders(String(en.get(key)));
      const actual = enPlaceholders(value);
      if (expected.size !== actual.size || [...expected].some((p) => !actual.has(p))) {
        issues.push(`${code}.${key}: 占位参数不一致（应为 {${[...expected]}}）`);
      }
    }
  }
  return issues;
}

function main() {
  const result = runGuard();
  let failed = false;
  if (result.unregisteredAreaFiles.length) {
    failed = true;
    console.error(
      `[i18n] 文案区域文件未在 register.ts 注册 ${result.unregisteredAreaFiles.length} 个:`,
    );
    for (const line of result.unregisteredAreaFiles) console.error(`  - ${line}`);
  }
  if (result.missingEn.length) {
    failed = true;
    console.error(`[i18n] 缺少 en 文案 ${result.missingEn.length} 个:`);
    for (const line of result.missingEn) console.error(`  - ${line}`);
  }
  if (result.missingZh.length) {
    failed = true;
    console.error(`[i18n] 缺少 zh-CN 文案 ${result.missingZh.length} 个:`);
    for (const line of result.missingZh) console.error(`  - ${line}`);
  }
  if (result.placeholderMismatches.length) {
    failed = true;
    console.error(
      `[i18n] en/zh-CN 占位参数不一致 ${result.placeholderMismatches.length} 个:`,
    );
    for (const line of result.placeholderMismatches) console.error(`  - ${line}`);
  }
  if (result.dynamicKeys.length) {
    failed = true;
    console.error(
      `[i18n] 复数/提示 helper 必须使用字面量键 ${result.dynamicKeys.length} 处:`,
    );
    for (const line of result.dynamicKeys) console.error(`  - ${line}`);
  }
  if (failed) {
    process.exit(1);
  }
  // 24 语言包 parity（键集合 / 键序 / 占位参数），漂移即失败。
  const { en } = collectRegisteredMessages(defaultRepoRoot, path.join(defaultRepoRoot, "ui", "src"));
  const packIssues = checkLocalePacks(en, defaultRepoRoot, path.join(defaultRepoRoot, "ui", "src"));
  if (packIssues.length) {
    console.error(`[i18n] 语言包与英文词典不一致 ${packIssues.length} 处:`);
    for (const line of packIssues) console.error(`  - ${line}`);
    process.exit(1);
  }
  console.log(
    `[i18n] OK: ${result.userKeyCount} 个用户可见消息键在 en 与 zh-CN 均已注册，占位参数一致；所有文案区域均已在 register.ts 注册。`,
  );
}

const invokedDirectly =
  process.argv[1] &&
  path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url));
if (invokedDirectly) {
  main();
}
