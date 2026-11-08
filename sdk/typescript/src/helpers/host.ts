import type { Context } from "../../index.d.ts";
import { commandEnvironment } from "../command.ts";
import type { HostServices } from "../routes.ts";
import { assertExecution, projectRoot } from "../scope.ts";
const bindings = new WeakMap<Context, HostServices>();
export function registerHelpers(ctx: Context, service: HostServices | undefined): void {
  if (service) bindings.set(ctx, service);
}
export async function helper<T>(ctx: Context, operation: string, args: readonly unknown[], signal = ctx.signal): Promise<T> {
  assertExecution();
  signal.throwIfAborted();
  let source: Context | undefined = ctx;
  while (source && !bindings.has(source)) source = Object.getPrototypeOf(source) as Context | undefined;
  const service = source ? bindings.get(source) : undefined;
  if (!service?.helper) throw new Error("host helper service unavailable");
  return (await service.helper(
    operation,
    args,
    projectRoot() || process.cwd(),
    signal,
    Object.entries({ ...process.env, ...commandEnvironment(ctx) })
      .filter((entry) => entry[1] !== undefined)
      .map(([name, value]) => `${name}=${value}`),
    ctx.stepId,
  )) as T;
}
