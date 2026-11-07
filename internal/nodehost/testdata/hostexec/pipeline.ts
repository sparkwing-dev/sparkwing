// The TypeScript SDK process the host exec tests and benchmark drive, mirroring the Go child.
import { spawn } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { definePipeline, main } from "../../../../sdk/typescript/src/index.ts";
import { runInput, withSignal } from "../../../../sdk/typescript/src/command.ts";

const secret = "hostexec-secret-value";
const dir = process.env.HOSTEXEC_DIR ?? "";

function observe(result, error) {
  const o = { stdout: result?.stdout ?? "", stderr: result?.stderr ?? "", exit: result?.exitCode ?? 0, error: "", terminated: "", not_found: false, survivor: false };
  if (error) {
    o.error = String(error.message);
    o.not_found = error.cause?.code === "ENOENT";
    o.terminated = error.terminated ?? "";
    o.exit = error.exitCode ?? o.exit;
    o.stdout = error.stdout ?? "";
    o.stderr = error.stderr ?? "";
  }
  return o;
}
async function run(promise) {
  try {
    return observe(await promise);
  } catch (error) {
    return observe(undefined, error);
  }
}
function alive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

const benchLine = "sparkwing host exec benchmark output line 0123456789 abcdefghijklmnopqrstuvwxy";

// bench mirrors the Go child's runHostExecBench: direct spawn with discarded output, run (logged) or capture.
async function bench(ctx) {
  const variant = process.env.HOSTEXEC_BENCH_VARIANT;
  const bulk = process.env.HOSTEXEC_BENCH_SIZE === "bulk";
  const count = Number(process.env.HOSTEXEC_BENCH_COUNT);
  const workers = Number(process.env.HOSTEXEC_BENCH_CONC);
  const argv = bulk ? ["sh", "-c", 'yes "$L" | head -c "$N"'] : ["/usr/bin/true"];
  const env = { L: benchLine, N: process.env.HOSTEXEC_BENCH_BYTES ?? "0" };
  const direct = () =>
    new Promise((resolve, reject) => {
      const child = spawn(argv[0], argv.slice(1), { env: { ...process.env, ...env }, stdio: ["ignore", "pipe", "pipe"] });
      let bytes = 0;
      child.stdout.on("data", (chunk) => (bytes += chunk.length));
      child.stderr.resume();
      child.once("error", reject);
      child.once("close", (code) => (code === 0 ? resolve(bytes) : reject(new Error(`exit ${code}`))));
    });
  const one = async () => {
    if (variant === "direct") return direct();
    if (variant === "logged") {
      await ctx.exec(argv, { env }).run();
      return 0;
    }
    return (await ctx.exec(argv, { env }).capture()).stdout.length;
  };
  for (let i = 0; i < (bulk ? 1 : Math.min(32, count)); i++) await one();
  const samples = new Array(count).fill(0);
  let next = 0;
  let bytes = 0;
  let errors = 0;
  let firstError = "";
  const cpu = process.cpuUsage();
  const start = process.hrtime.bigint();
  await Promise.all(
    Array.from({ length: workers }, async () => {
      for (let i = next++; i < count; i = next++) {
        const began = process.hrtime.bigint();
        try {
          // safety: `bytes += await one()` reads bytes before awaiting and loses concurrent updates.
          const counted = await one();
          bytes += counted;
        } catch (error) {
          errors++;
          firstError ||= String(error.message);
        }
        samples[i] = Number(process.hrtime.bigint() - began);
      }
    }),
  );
  const wall = Number(process.hrtime.bigint() - start);
  const used = process.cpuUsage(cpu);
  return { samples_ns: samples, wall_ns: wall, bytes, sdk_cpu_ns: (used.user + used.system) * 1000, errors, ...(firstError ? { first_error: firstError } : {}) };
}

definePipeline({
  name: "hostexec",
  plan(plan) {
    plan.job("bench").step("run", bench);
    const job = plan.job("job");
    job.step("logged", (ctx) => run(ctx.bash(`printf 'line one\\n${secret} two\\n'; printf 'err text' >&2`).run()));
    job.step("captured", (ctx) => run(ctx.exec(["sh", "-c", "printf captured-out; printf captured-err >&2"]).capture()));
    job.step("big", async (ctx) => {
      const o = await run(ctx.bash("head -c 300000 /dev/zero | tr '\\0' a").capture());
      return { ...o, stdout: String(o.stdout.length), stderr: "" };
    });
    job.step("missing", (ctx) => run(ctx.exec(["sparkwing-hostexec-missing-command"]).run()));
    job.step("failing", (ctx) => run(ctx.bash("echo boom >&2; exit 3").run()));
    job.step("env", (ctx) => {
      process.env.HOSTEXEC_SET = "from-pipeline";
      return run(ctx.bash(`printf '%s|%s|%s' "$HOSTEXEC_SET" "$EXPLICIT" "\${SPARKWING_NODE_TOKEN-unset}"`).env("EXPLICIT", "x").env("SPARKWING_NODE_TOKEN", "leak").capture());
    });
    job.step("dir", (ctx) => run(ctx.exec(["pwd"]).dir(join(dir, "sub")).capture()));
    job.step("stdin", (ctx) => run(runInput(ctx, ["sh", "-c", "exec 3<&0; sleep 30 <&3 & exit 0"], "x".repeat(200000))));
    job.step("lookup", (ctx) => run(ctx.exec(["swlookup"]).env("PATH", "bin:/usr/bin:/bin").dir(dir).capture()));
    job.step("timeout", async (ctx) => {
      const pidFile = join(dir, "grandchild.pid");
      const o = await run(withSignal(ctx, AbortSignal.timeout(300)).bash(`sleep 30 & echo $! > "$P"; wait`).env("P", pidFile).run());
      const pid = existsSync(pidFile) ? Number(readFileSync(pidFile, "utf8").trim()) : 0;
      const deadline = Date.now() + 2000;
      while (pid && alive(pid) && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 10));
      return { ...o, survivor: pid === 0 || alive(pid) };
    });
  },
});
await main();
