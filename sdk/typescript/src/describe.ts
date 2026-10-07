// The describe and plan documents. describe is static: it names the protocol
// this SDK speaks and the pipelines it holds. The plan document depends on
// a run's args, so the engine asks for it per run over stdio.

import { pipelines } from "./plan.ts";
import type { LineSink } from "./log.ts";

export const PROTOCOL_VERSION = 1;
export const SDK_VERSION = "0.0.0";

export interface DescribeArg {
  name: string;
  type: string;
  required: boolean;
  desc?: string;
  default?: string;
  secret?: boolean;
}

export interface DescribePipeline {
  name: string;
  short?: string;
  help?: string;
  args: DescribeArg[];
}

export interface DescribeDoc {
  protocol: number;
  sdk: { language: "typescript"; version: string };
  pipelines: DescribePipeline[];
}

export interface PlanNode {
  id: string;
  needs: string[];
  retry?: { attempts: number; backoff_ms?: number };
  timeout_ms?: number;
  steps?: Array<{ id: string; needs: string[] }>;
  closures?: { skip_if?: string };
}

export interface PlanDoc {
  pipeline: string;
  nodes: PlanNode[];
}

export function describe(): DescribeDoc {
  return {
    protocol: PROTOCOL_VERSION,
    sdk: { language: "typescript", version: SDK_VERSION },
    pipelines: pipelines().map((p) => {
      const out: DescribePipeline = {
        name: p.name,
        args: (p.args ?? []).map((a) => {
          const arg: DescribeArg = { name: a.name, type: a.type ?? "string", required: a.required ?? false };
          if (a.desc !== undefined) arg.desc = a.desc;
          if (a.default !== undefined) arg.default = a.default;
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
