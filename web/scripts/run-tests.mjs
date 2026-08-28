import { spawnSync } from "node:child_process";
import { readdirSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const webRoot = fileURLToPath(new URL("../", import.meta.url));
const sourceRoot = join(webRoot, "src");

function findTests(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      return findTests(path);
    }
    return entry.isFile() && /\.test\.tsx?$/.test(entry.name) ? [path] : [];
  });
}

const tests = findTests(sourceRoot).sort();
if (tests.length === 0) {
  console.error("frontend unit suite: no .test.ts or .test.tsx files found");
  process.exit(1);
}

const result = spawnSync(
  process.execPath,
  ["--import", "tsx", "--test", "--test-reporter=tap", ...tests],
  {
    cwd: webRoot,
    encoding: "utf8",
  },
);
if (result.error) {
  throw result.error;
}
process.stdout.write(result.stdout);
process.stderr.write(result.stderr);
if (result.status !== 0) {
  process.exit(result.status ?? 1);
}
if (/^1\.\.0\r?$/m.test(result.stdout)) {
  console.error("frontend unit suite: Node discovered zero tests");
  process.exit(1);
}
