import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { access, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";

const repositoryRoot = join(__dirname, "..", "..", "..");

type StartedFixture = {
  origin: string;
  controller_origin: string;
  service_token: string;
};

export type AuthenticatedDashboard = StartedFixture & {
  close: () => Promise<void>;
};

type StartOptions = {
  buildCommand?: string;
  buildArgs?: string[];
  buildTimeoutMs?: number;
  temporaryParent?: string;
};

async function stop(child: ChildProcessWithoutNullStreams): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null) return;
  const exited = new Promise<void>((resolve) =>
    child.once("close", () => resolve()),
  );
  child.kill("SIGTERM");
  await Promise.race([
    exited,
    new Promise<void>((resolve) => setTimeout(resolve, 5_000)),
  ]);
  if (child.exitCode === null && child.signalCode === null) {
    child.kill("SIGKILL");
    await exited;
  }
}

async function run(
  command: string,
  args: string[],
  timeoutMs: number,
): Promise<void> {
  const child = spawn(command, args, {
    cwd: repositoryRoot,
    env: { ...process.env, GOWORK: "off" },
  });
  try {
    let stderr = "";
    child.stderr.on("data", (chunk) => {
      stderr += String(chunk);
    });
    let timeout: ReturnType<typeof setTimeout> | undefined;
    let outcome: { code: number | null; signal: NodeJS.Signals | null } | "timeout";
    try {
      outcome = await Promise.race([
        new Promise<{ code: number | null; signal: NodeJS.Signals | null }>(
          (resolve, reject) => {
            child.once("error", reject);
            child.once("close", (code, signal) => resolve({ code, signal }));
          },
        ),
        new Promise<"timeout">((resolve) => {
          timeout = setTimeout(() => resolve("timeout"), timeoutMs);
        }),
      ]);
    } finally {
      if (timeout) clearTimeout(timeout);
    }
    if (outcome === "timeout") {
      throw new Error(`${command} timed out after ${timeoutMs}ms`);
    }
    if (outcome.code !== 0) {
      throw new Error(
        `${command} exited ${outcome.code ?? outcome.signal}: ${stderr}`,
      );
    }
  } catch (error) {
    await stop(child);
    throw error;
  }
}

async function waitForStart(
  child: ChildProcessWithoutNullStreams,
): Promise<StartedFixture> {
  let stderr = "";
  child.stderr.on("data", (chunk) => {
    stderr += String(chunk);
  });
  return new Promise<StartedFixture>((resolve, reject) => {
    const lines = createInterface({ input: child.stdout });
    const timeout = setTimeout(() => {
      reject(new Error(`authenticated dashboard did not start: ${stderr}`));
    }, 30_000);
    const onExit = (code: number | null) => {
      clearTimeout(timeout);
      reject(new Error(`authenticated dashboard exited ${code}: ${stderr}`));
    };
    child.once("exit", onExit);
    lines.once("line", (line) => {
      clearTimeout(timeout);
      child.off("exit", onExit);
      lines.close();
      try {
        resolve(JSON.parse(line) as StartedFixture);
      } catch (error) {
        reject(
          new Error(`invalid authenticated dashboard address: ${line}`, {
            cause: error,
          }),
        );
      }
    });
  });
}

export async function startAuthenticatedDashboard(
  options: StartOptions = {},
): Promise<AuthenticatedDashboard> {
  const output = join(repositoryRoot, "web", "out");
  await access(join(output, "index.html"));

  const temporary = await mkdtemp(
    join(options.temporaryParent ?? tmpdir(), "sparkwing-auth-browser-"),
  );
  const binary = join(
    temporary,
    process.platform === "win32" ? "browser-fixture.exe" : "browser-fixture",
  );
  let child: ChildProcessWithoutNullStreams | undefined;
  try {
    await run(
      options.buildCommand ?? "go",
      options.buildArgs ?? [
        "build",
        "-o",
        binary,
        "./internal/web/browserfixture",
      ],
      options.buildTimeoutMs ?? 60_000,
    );
    const fixture = spawn(binary, [], {
      cwd: repositoryRoot,
      env: {
        ...process.env,
        SPARKWING_BROWSER_FIXTURE_HOME: join(temporary, "fixture-home"),
        SPARKWING_BROWSER_WEB_OUT: output,
        SPARKWING_WEB_INSECURE_COOKIES: "1",
      },
    });
    child = fixture;
    const started = await waitForStart(fixture);
    return {
      ...started,
      close: async () => {
        await stop(fixture);
        await rm(temporary, { recursive: true, force: true });
      },
    };
  } catch (error) {
    if (child) await stop(child);
    await rm(temporary, { recursive: true, force: true });
    throw error;
  }
}
