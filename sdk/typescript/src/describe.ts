// The describe and plan documents, shaped by the node protocol's describe
// schema. describe is static: the protocol version, this SDK and the
// pipelines. The plan document depends on a run's args, so the engine asks
// for it per run.

import { pipelines } from "./plan.ts";

export const PROTOCOL_VERSION = "1";

/** This package's version, sent in describe so a refusal can name the SDK to upgrade. */
export const SDK_VERSION = "0.0.0";

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
  sdk: { language: string; version: string };
  pipelines: DescribePipeline[];
}

export interface Closures {
  skip_if?: string[];
  before_run?: string[];
  after_run?: string[];
  verify?: string;
  cache_key?: string;
  generator?: string[];
}

export interface PlanStep {
  id: string;
  needs?: string[];
  is_result?: boolean;
  has_skip_if?: boolean;
  finally?: boolean;
  continue_on_error?: boolean;
  optional?: boolean;
  has_dry_run?: boolean;
  safe_without_dry_run?: boolean;
  closures?: Closures;
}

export interface Modifiers {
  retry?: number;
  retry_backoff_ms?: number;
  retry_auto?: boolean;
  timeout_ms?: number;
  no_progress_timeout_ms?: number;
  runs_on?: string[];
  prefers?: string[];
  cache?: boolean;
  cache_ttl_ms?: number;
  conc_group?: string;
  conc_capacity?: number;
  conc_cost?: number;
  conc_scope?: string;
  conc_on_limit?: string;
  conc_queue_timeout_ms?: number;
  conc_cancel_timeout_ms?: number;
  res_cores?: number;
  res_memory_bytes?: number;
  optional?: boolean;
  continue_on_error?: boolean;
  on_failure?: string;
  has_before_run?: boolean;
  has_after_run?: boolean;
  has_skip_if?: boolean;
  has_verify?: boolean;
  skip_if_budget_ms?: number;
}

export interface CacheSpec {
  name: string;
  path?: string;
  resolver?: "gomod" | "npm";
  key_scope?: string;
  key_files: string[];
}

export interface ApprovalSpec {
  message?: string;
  timeout_ms?: number;
  on_timeout?: "fail" | "deny" | "approve";
}

export interface PlanNode {
  id: string;
  deps: string[];
  env?: Record<string, string>;
  dynamic?: boolean;
  approval?: ApprovalSpec;
  on_failure_of?: string;
  modifiers?: Modifiers;
  caches?: CacheSpec[];
  memo_key?: { closure: string };
  work?: { steps: PlanStep[]; result_step?: string; failure_policy?: "fail-fast" | "collect-all" };
  closures?: Closures;
  needs_generated?: string[];
}

export interface PlanConcurrency {
  key: string;
  capacity?: number;
  cost?: number;
  on_limit?: string;
  queue_timeout_ms?: number;
  cancel_timeout_ms?: number;
}

export interface PlanDoc {
  protocol: string;
  pipeline: string;
  run_id: string;
  nodes: PlanNode[];
  plan_concurrency?: PlanConcurrency;
  plan_concurrency_groups?: PlanConcurrency[];
  plan_resources?: { cores?: number; memory_bytes?: number };
}

export function describe(): DescribeDoc {
  return {
    protocol: PROTOCOL_VERSION,
    sdk: { language: "typescript", version: SDK_VERSION },
    pipelines: pipelines().map((p) => {
      const out: DescribePipeline = {
        name: p.name,
        args: (p.args ?? []).map((a) => {
          const arg: DescribeArg = {
            name: a.name,
            go_name: a.name,
            type: a.type ?? "string",
            required: a.required ?? false,
          };
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
