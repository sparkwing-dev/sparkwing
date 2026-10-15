export interface ErrorFields {
  code: string;
  message: string;
  hint?: string;
  details?: Readonly<Record<string, unknown>>;
}

export class SDKError extends Error {
  readonly code: string;
  readonly hint?: string;
  readonly details?: Readonly<Record<string, unknown>>;
  constructor(message: string, options: Omit<ErrorFields, "message"> = { code: "sdk_error" }) {
    super(message);
    this.name = "SDKError";
    this.code = options.code;
    if (options.hint !== undefined) this.hint = options.hint;
    if (options.details !== undefined) this.details = options.details;
  }
}
export class InputError extends Error {
  override readonly cause: unknown;
  constructor(cause: unknown) {
    super(messageOf(cause), { cause });
    this.name = "InputError";
    this.cause = cause;
  }
}
export class VerifyError extends Error {
  override readonly cause: unknown;
  constructor(cause: unknown) {
    super(messageOf(cause), { cause });
    this.name = "VerifyError";
    this.cause = cause;
  }
}
export class StepError extends Error {
  readonly stepId: string;
  override readonly cause: unknown;
  constructor(stepId: string, cause: unknown) {
    super(messageOf(cause), { cause });
    this.name = "StepError";
    this.stepId = stepId;
    this.cause = cause;
  }
}
export class NoProjectError extends Error {}
export function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
export function failure(code: string, message: string, details?: Record<string, unknown>): SDKError {
  return new SDKError(message, { code, ...(details ? { details } : {}) });
}
export function errorFields(error: unknown): ErrorFields {
  if (error instanceof SDKError)
    return {
      code: error.code,
      message: error.message,
      ...(error.hint === undefined ? {} : { hint: error.hint }),
      ...(error.details === undefined ? {} : { details: error.details }),
    };
  return { code: "sdk_error", message: messageOf(error) };
}
export function decodeError(value: unknown): SDKError {
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw failure("protocol_error", "invalid error object");
  const v = value as Record<string, unknown>;
  if (
    typeof v.code !== "string" ||
    v.code === "" ||
    typeof v.message !== "string" ||
    "error" in v ||
    (v.hint !== undefined && typeof v.hint !== "string")
  )
    throw failure("protocol_error", "invalid error object");
  if (v.details !== undefined && (v.details === null || typeof v.details !== "object" || Array.isArray(v.details)))
    throw failure("protocol_error", "invalid error details");
  const details = v.details as Record<string, unknown> | undefined;
  return new SDKError(v.message, {
    code: v.code,
    ...(v.hint === undefined ? {} : { hint: v.hint as string }),
    ...(details === undefined ? {} : { details }),
  });
}
export function object(value: unknown, label: string): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw failure("bad_request", `${label} must be an object`);
  return value as Record<string, unknown>;
}
export function string(value: unknown, label: string): string {
  if (typeof value !== "string" || value === "") throw failure("bad_request", `${label} must be a nonempty string`);
  return value;
}
