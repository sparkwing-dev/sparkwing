export interface PlanDoc {
  jobs: JobSpec[];
  concurrency?: ConcurrencySpec[];
  priority?: number;
  claim_wait_ms?: number;
  admission_class?: string;
  source?: SourceSpec;
}

export interface JobSpec {
  id: string;
  deps?: string[];
  deps_if_present?: string[];
  needs_generated?: string[];
  groups?: string[];
  env?: Record<string, { value: string; startup?: boolean }>;
  artifacts?: string[];
  consumes?: { producer: string; into?: string }[];
  pipeline_refs?: { pipeline: string; job: string }[];
  children?: { pipeline: string; repository?: string }[];
  approval?: { message?: string; timeout_ms?: number; on_timeout?: string };
  on_failure_of?: string;
  retry?: { attempts: number; backoff_ms?: number };
  timeout_ms?: number;
  no_progress_timeout_ms?: number;
  requires?: string[];
  prefers?: string[];
  inline?: boolean;
  optional?: boolean;
  continue_on_error?: boolean;
  memo?: { ttl_ms?: number };
  caches?: CacheSpec[];
  concurrency?: ConcurrencySpec[];
  closures?: ClosureSpec;
  skip_if_budget_ms?: number;
  steps?: StepSpec[];
  result_step?: string;
  failure_policy?: string;
}

export interface StepSpec {
  id: string;
  needs?: string[];
  groups?: string[];
  finally?: boolean;
  risks?: string[];
  optional?: boolean;
  continue_on_error?: boolean;
  has_dry_run?: boolean;
  safe_without_dry_run?: boolean;
  closures?: ClosureSpec;
}

export interface ClosureSpec {
  skip_if?: string[];
  before_run?: string[];
  verify?: string;
  after_run?: string[];
  memo_key?: string;
  generate?: string[];
}

export interface ConcurrencySpec {
  group: string;
  scope?: string;
  capacity?: number;
  cost?: number;
  on_limit?: string;
  queue_timeout_ms?: number;
  cancel_timeout_ms?: number;
}

export interface SourceSpec {
  full_history?: boolean;
  depth?: number;
  tags?: boolean;
  submodules?: boolean;
  lfs?: boolean;
}
export interface CacheSpec {
  name: string;
  path?: string;
  resolver?: string;
  key_scope?: string;
  key_files: string[];
}
