import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));

interface Entry {
  readonly name: string;
  readonly signature: string;
  readonly doc: string;
  readonly members: readonly { signature: string; doc: string }[];
}

// The declarations' comments open with a review tag; readers need only the sentence.
function docOf(node: ts.Node, source: ts.SourceFile): string {
  const ranges = ts.getLeadingCommentRanges(source.text, node.getFullStart()) ?? [];
  const last = ranges.at(-1);
  if (!last || !source.text.startsWith("/**", last.pos)) return "";
  return source.text
    .slice(last.pos + 3, last.end - 2)
    .split("\n")
    .map((line) => line.replace(/^\s*\*\s?/, "").trim())
    .join(" ")
    .trim()
    .replace(/^(contract|safety|perf|hack|bug):\s*/, "");
}

function oneLine(text: string): string {
  return text
    .replace(/\/\*\*[\s\S]*?\*\//g, "")
    .replace(/\s+/g, " ")
    .replace(/;$/, "")
    .trim();
}

function membersOf(node: ts.Node, source: ts.SourceFile): { signature: string; doc: string }[] {
  let members: readonly ts.Node[] = [];
  if (ts.isInterfaceDeclaration(node) || ts.isClassDeclaration(node)) members = node.members;
  else if (ts.isModuleDeclaration(node) && node.body && ts.isModuleBlock(node.body)) members = node.body.statements;
  return members
    .filter((member) => !ts.getModifiers(member as ts.HasModifiers)?.some((m) => m.kind === ts.SyntaxKind.PrivateKeyword))
    .map((member) => ({ signature: oneLine(member.getText(source)).replace(/^declare /, ""), doc: docOf(member, source) }));
}

function heading(node: ts.Node, source: ts.SourceFile): string {
  if (ts.isInterfaceDeclaration(node) || ts.isClassDeclaration(node)) {
    const text = node.getText(source);
    return oneLine(text.slice(0, text.indexOf("{"))).replace(/^export (declare )?/, "");
  }
  if (ts.isModuleDeclaration(node)) return `namespace ${node.name.getText(source)}`;
  return oneLine(node.getText(source)).replace(/^export (declare )?/, "");
}

export function renderReference(): string {
  const path = join(packageRoot, "index.d.ts");
  const source = ts.createSourceFile(path, readFileSync(path, "utf8"), ts.ScriptTarget.ES2023, true);
  const groups = new Map<string, Entry[]>([
    ["Functions", []],
    ["Classes", []],
    ["Interfaces", []],
    ["Types", []],
    ["Constants", []],
    ["Namespaces", []],
  ]);
  for (const statement of source.statements) {
    if (!ts.getModifiers(statement as ts.HasModifiers)?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword)) continue;
    const doc = docOf(statement, source);
    const add = (group: string, name: string, members: Entry["members"] = []): void => {
      groups.get(group)?.push({ name, signature: heading(statement, source), doc, members });
    };
    if (ts.isFunctionDeclaration(statement) && statement.name) add("Functions", statement.name.text);
    else if (ts.isClassDeclaration(statement) && statement.name) add("Classes", statement.name.text, membersOf(statement, source));
    else if (ts.isInterfaceDeclaration(statement)) add("Interfaces", statement.name.text, membersOf(statement, source));
    else if (ts.isTypeAliasDeclaration(statement)) add("Types", statement.name.text);
    else if (ts.isVariableStatement(statement)) add("Constants", statement.declarationList.declarations.map((d) => d.name.getText(source)).join(", "));
    else if (ts.isModuleDeclaration(statement)) add("Namespaces", statement.name.getText(source), membersOf(statement, source));
  }
  const out = [
    "<!-- GENERATED from sdk/typescript/index.d.ts by sdk/typescript/tools/reference.ts. Do not edit by hand; regenerate with `bash bin/gen-ts-sdk-docs.sh`. -->",
    "<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->",
    "# TypeScript SDK API reference",
    "",
    "Every export of `@sparkwing/sdk`, generated from the package's `index.d.ts`, which is also what your editor reads from `node_modules/@sparkwing/sdk`. For concepts and examples in Go and TypeScript side by side, see [sdk.md](sdk.md). The Go package has its own reference, [sdk-reference.md](sdk-reference.md).",
  ];
  const describe = (doc: string): string => (doc === "" ? "" : ` -- ${doc}`);
  for (const [group, entries] of groups) {
    if (entries.length === 0) continue;
    out.push("", `## ${group}`, "");
    const sorted = [...entries].sort((a, b) => a.name.localeCompare(b.name));
    if (group === "Functions" || group === "Types" || group === "Constants") {
      for (const entry of sorted) out.push(`- \`${entry.signature}\`${describe(entry.doc)}`);
      continue;
    }
    for (const entry of sorted) {
      out.push(`### ${entry.name}`, "", `\`${entry.signature}\``, "");
      if (entry.doc !== "") out.push(entry.doc, "");
      for (const member of entry.members) out.push(`- \`${member.signature}\`${describe(member.doc)}`);
      if (entry.members.length > 0) out.push("");
    }
    if (out.at(-1) === "") out.pop();
  }
  return out.join("\n") + "\n";
}

if (process.argv[1] === fileURLToPath(import.meta.url)) process.stdout.write(renderReference());
