// The describe and plan documents, shaped by the node protocol's describe
// schema. describe is static: the protocol version and the pipelines. The
// plan document depends on a run's args, so the engine asks for it per run.

import { pipelines } from "./plan.ts";
import type { LineSink } from "./log.ts";

export const PROTOCOL_VERSION = "1";

export interface DescribeArg {
  name: string;
  go_name: string;
  type: "string" | "bool" | "int";
  required: boolean;
  desc?: string;
  default?: string;
  enum?: string[];
  secret?: boolean;
}

export interface DescribePipeline {
  name: string;
  short?: string;
  help?: string;
  args: DescribeArg[];
}

export interface DescribeDoc {
  protocol: string;
  pipelines: DescribePipeline[];
}

export interface Closures {
  skip_if?: string[];
}

export interface PlanStep {
  id: string;
  needs?: string[];
}

export interface Modifiers {
  retry?: number;
  retry_backoff_ms?: number;
  timeout_ms?: number;
  has_skip_if?: boolean;
}

export interface PlanNode {
  id: string;
  deps: string[];
  modifiers?: Modifiers;
  work?: { steps: PlanStep[] };
  closures?: Closures;
}

export interface PlanDoc {
  protocol: string;
  pipeline: string;
  run_id: string;
  nodes: PlanNode[];
}

export function describe(): DescribeDoc {
  return {
    protocol: PROTOCOL_VERSION,
    pipelines: pipelines().map((p) => {
      const out: DescribePipeline = {
        name: p.name,
        args: (p.args ?? []).map((a) => {
          const arg: DescribeArg = { name: a.name, go_name: a.name, type: a.type ?? "string", required: a.required ?? false };
          if (a.desc !== undefined) arg.desc = a.desc;
          if (a.default !== undefined) arg.default = a.default;
          if (a.enum !== undefined) arg.enum = [...a.enum];
          if (a.secret) arg.secret = true;
          return arg;
        }),
      };
      if (p.short !== undefined) out.short = p.short;
      if (p.help !== undefined) out.help = p.help;
      return out;
    }),
  };
}

export function emitDescribe(out: LineSink): void {
  out.write(JSON.stringify(describe()) + "\n");
}
