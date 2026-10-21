import type { ArgSpec, RunContext } from "../index.d.ts";
import { resolveArgs, validateArgSpecs } from "./args.ts";
import { Plan, pipelines } from "./plan.ts";
import { SDKError, errorFields, type ErrorFields } from "./errors.ts";

export interface DescribeArg {
  name: string;
  short?: string;
  type: string;
  required: boolean;
  desc?: string;
  default?: string;
  enum?: string[];
  secret?: boolean;
  job_id?: string;
}
export interface DescribePipeline {
  name: string;
  error?: ErrorFields;
  short?: string;
  help?: string;
  examples?: { command: string; comment: string }[];
  args: DescribeArg[];
  env_vars?: { name: string; description?: string; default?: string }[];
  extra?: boolean;
  risks?: string[];
  risks_by_step?: { job_id: string; step_id: string; labels: string[] }[];
  secrets?: { name: string; optional?: boolean }[];
}
export interface DescribeDoc {
  pipelines: DescribePipeline[];
}

export function argument(spec: ArgSpec, jobID?: string): DescribeArg {
  const arg: DescribeArg = { name: spec.name, type: spec.type, required: spec.required ?? false };
  if (spec.short !== undefined) arg.short = spec.short;
  if (spec.description !== undefined) arg.desc = spec.description;
  if (spec.default !== undefined) arg.default = spec.default;
  if (spec.enum !== undefined) arg.enum = [...spec.enum];
  if (spec.secret !== undefined) arg.secret = spec.secret;
  if (jobID !== undefined) arg.job_id = jobID;
  return arg;
}

export async function describe(run: RunContext): Promise<DescribeDoc> {
  const descriptions: DescribePipeline[] = [];
  const definitions = pipelines();
  for (const definition of definitions) {
    const plan = new Plan();
    try {
      const entry: DescribePipeline = { name: definition.name, args: (definition.args ?? []).map((spec) => argument(spec)) };
      if (definition.short !== undefined) entry.short = definition.short;
      const help = definition.help ?? definition.description;
      if (help !== undefined) entry.help = help;
      if (definition.extra !== undefined) entry.extra = definition.extra;
      if (definition.examples)
        entry.examples = definition.examples.map((example) => ({ command: example.command, comment: example.comment ?? "" }));
      if (definition.envVars) entry.env_vars = definition.envVars.map((env) => ({ ...env }));
      if (definition.secrets)
        entry.secrets = definition.secrets.map((secret) => ({
          name: secret.name,
          ...(secret.optional === undefined ? {} : { optional: secret.optional }),
        }));
      const args = resolveArgs(definition.args ?? [], {}, { allowMissing: true });
      await definition.plan(plan, { ...run, pipeline: definition.name, args });
      validateArgSpecs([...(definition.args ?? []), ...plan.jobs().flatMap((job) => job.argSpecs())]);
      const risks = new Set<string>();
      const byStep: NonNullable<DescribePipeline["risks_by_step"]> = [];
      for (const job of plan.jobs()) {
        entry.args.push(...job.argSpecs().map((spec) => argument(spec, job.id)));
        for (const step of job.steps()) {
          const labels = step.toSpec().risks ?? [];
          if (labels.length) byStep.push({ job_id: job.id, step_id: step.id, labels });
          for (const label of labels) risks.add(label);
        }
      }
      if (risks.size) entry.risks = [...risks].sort();
      if (byStep.length) entry.risks_by_step = byStep;
      plan.seal();
      descriptions.push(entry);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      descriptions.push({ name: definition.name, args: [], error: errorFields(new SDKError(`describe pipeline ${JSON.stringify(definition.name)}: ${message}`, { code: "plan_failed" })) });
    }
  }
  return { pipelines: descriptions };
}
