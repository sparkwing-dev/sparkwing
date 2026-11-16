import { existsSync, readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

export const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));

export interface Snippet {
  readonly file: string;
  readonly line: number;
  readonly body: string;
  readonly skip?: string;
}

export interface SnippetFailure {
  readonly snippet: Snippet;
  readonly messages: readonly string[];
}

const fence = /^```ts(\s|$)/;
const skipMarker = /<!--\s*doccheck:skip\s+(.*?)\s*-->/;
// Migration guides and changelog fragments quote APIs that no longer exist.
const skippedDirs = new Set(["migrations", "changes"]);

export function extractSnippets(root: string): Snippet[] {
  const out: Snippet[] = [];
  const walk = (dir: string): void => {
    for (const entry of readdirSync(dir, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
      const path = join(dir, entry.name);
      if (entry.isDirectory()) {
        if (!skippedDirs.has(entry.name)) walk(path);
        continue;
      }
      if (!entry.name.endsWith(".md")) continue;
      const lines = readFileSync(path, "utf8").split("\n");
      for (let i = 0; i < lines.length; i++) {
        if (!fence.test(lines[i] ?? "")) continue;
        let skip: string | undefined;
        for (let k = i - 1; k >= 0; k--) {
          const text = (lines[k] ?? "").trim();
          if (text === "") continue;
          skip = skipMarker.exec(text)?.[1];
          break;
        }
        let end = i + 1;
        while (end < lines.length && !(lines[end] ?? "").startsWith("```")) end++;
        const snippet = { file: relative(root, path), line: i + 2, body: lines.slice(i + 1, end).join("\n") };
        out.push(skip === undefined ? snippet : { ...snippet, skip });
        i = end;
      }
    }
  };
  walk(root);
  return out;
}

function sdkExports(): { values: string[]; types: string[] } {
  const source = ts.createSourceFile("index.d.ts", readFileSync(join(packageRoot, "index.d.ts"), "utf8"), ts.ScriptTarget.ES2023);
  const values: string[] = [];
  const types: string[] = [];
  for (const statement of source.statements) {
    if (!ts.getModifiers(statement as ts.HasModifiers)?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword)) continue;
    if (ts.isInterfaceDeclaration(statement) || ts.isTypeAliasDeclaration(statement)) types.push(statement.name.text);
    else if (ts.isVariableStatement(statement)) {
      for (const declaration of statement.declarationList.declarations) if (ts.isIdentifier(declaration.name)) values.push(declaration.name.text);
    } else if (
      (ts.isFunctionDeclaration(statement) || ts.isClassDeclaration(statement) || ts.isModuleDeclaration(statement)) &&
      statement.name !== undefined &&
      ts.isIdentifier(statement.name)
    )
      values.push(statement.name.text);
  }
  return { values: [...new Set(values)], types: [...new Set(types)] };
}

// A block with its own imports is a complete module; anything else is a
// fragment of a plan callback, given the names the docs use for its handles.
function wrap(snippet: Snippet, header: string): { source: string; offset: number } {
  if (/^import\s/m.test(snippet.body)) return { source: snippet.body + "\n", offset: 0 };
  const prelude = header + "declare const plan: Plan;\ndeclare const ctx: Context;\ndeclare const run: RunContext;\nexport async function __snippet(): Promise<void> {\n";
  return { source: prelude + snippet.body + "\n}\n", offset: prelude.split("\n").length - 1 };
}

export function checkSnippets(snippets: readonly Snippet[]): SnippetFailure[] {
  const { values, types } = sdkExports();
  const header = `import { ${values.join(", ")} } from "@sparkwing/sdk";\nimport type { ${types.join(", ")} } from "@sparkwing/sdk";\n`;
  const dir = join(packageRoot, ".doc-snippets");
  const files = new Map<string, { snippet: Snippet; source: string; offset: number }>();
  snippets.forEach((snippet, index) => {
    if (snippet.skip === undefined) files.set(join(dir, `snippet${index}.ts`), { snippet, ...wrap(snippet, header) });
  });
  const config = ts.readConfigFile(join(packageRoot, "tsconfig.json"), ts.sys.readFile);
  const { options } = ts.parseJsonConfigFileContent(config.config, ts.sys, packageRoot);
  const host = ts.createCompilerHost({ ...options, noEmit: true });
  host.getCurrentDirectory = () => packageRoot;
  const getSourceFile = host.getSourceFile.bind(host);
  host.getSourceFile = (name, language, onError, create) => {
    const file = files.get(name);
    return file ? ts.createSourceFile(name, file.source, language) : getSourceFile(name, language, onError, create);
  };
  const fileExists = host.fileExists.bind(host);
  host.fileExists = (name) => files.has(name) || fileExists(name);
  const directoryExists = host.directoryExists?.bind(host);
  host.directoryExists = (name) => name === dir || (directoryExists ? directoryExists(name) : existsSync(name));
  const program = ts.createProgram([...files.keys()], { ...options, noEmit: true }, host);
  const failures = new Map<string, string[]>();
  for (const diagnostic of ts.getPreEmitDiagnostics(program)) {
    const name = diagnostic.file?.fileName;
    const file = name === undefined ? undefined : files.get(name);
    const text = ts.flattenDiagnosticMessageText(diagnostic.messageText, "\n");
    if (!file || name === undefined || diagnostic.start === undefined || diagnostic.file === undefined) {
      failures.set("", [...(failures.get("") ?? []), text]);
      continue;
    }
    const { line } = diagnostic.file.getLineAndCharacterOfPosition(diagnostic.start);
    const where = `${file.snippet.file}:${file.snippet.line + line - file.offset}`;
    failures.set(name, [...(failures.get(name) ?? []), `${where}: ${text}`]);
  }
  return [...failures.entries()].map(([name, messages]) => ({
    snippet: files.get(name)?.snippet ?? { file: "(program)", line: 0, body: "" },
    messages,
  }));
}
