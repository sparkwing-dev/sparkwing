import type { ArgSpec, Args } from "../index.d.ts";

const types = new Set(["string", "bool", "int", "int64", "float64", "duration", "[]string"]);
const booleans = new Set(["1", "t", "T", "TRUE", "true", "True", "0", "f", "F", "FALSE", "false", "False"]);
const integer = /^[+-]?\d+$/;
const durationPattern = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|µs|μs|ms|s|m|h)(?:(?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|µs|μs|ms|s|m|h))*$/;

function validDuration(value: string): boolean {
  if (/^[+-]?0$/.test(value)) return true;
  if (!durationPattern.test(value)) return false;
  const units: Record<string, bigint> = {
    ns: 1n,
    us: 1000n,
    µs: 1000n,
    μs: 1000n,
    ms: 1000000n,
    s: 1000000000n,
    m: 60000000000n,
    h: 3600000000000n,
  };
  let nanos = 0n;
  for (const match of value.matchAll(/(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/g)) {
    const [whole = "0", fraction = ""] = match[1]!.split(".");
    const unit = units[match[2]!]!;
    nanos += BigInt(whole || "0") * unit;
    if (fraction) nanos += (BigInt(fraction) * unit) / 10n ** BigInt(fraction.length);
  }
  return nanos <= (value.startsWith("-") ? 2n ** 63n : 2n ** 63n - 1n);
}

function validateValue(spec: ArgSpec, value: string): string {
  let valid = typeof value === "string";
  if (valid)
    switch (spec.type) {
      case "bool":
        valid = booleans.has(value);
        break;
      case "int":
      case "int64":
        valid = integer.test(value) && BigInt(value) >= -(2n ** 63n) && BigInt(value) < 2n ** 63n;
        break;
      case "float64":
        valid = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/.test(value) && Number.isFinite(Number(value));
        break;
      case "duration":
        valid = validDuration(value);
        break;
      case "[]string":
        value = value
          .split(",")
          .map((item) => item.trim())
          .filter(Boolean)
          .join(",");
        break;
    }
  if (!valid) throw new Error(`argument --${spec.name} must be ${spec.type}`);
  const values = spec.type === "[]string" ? value.split(",").filter(Boolean) : [value];
  if (spec.enum?.length && values.some((item) => !spec.enum!.includes(item)))
    throw new Error(`argument --${spec.name} is not an allowed value`);
  return value;
}

export function validateArgSpecs(specs: readonly ArgSpec[]): void {
  const names = new Set<string>();
  const shorts = new Set<string>();
  for (const spec of specs) {
    if (!spec.name) throw new Error("argument name must not be empty");
    if (names.has(spec.name)) throw new Error(`duplicate argument --${spec.name}`);
    names.add(spec.name);
    if (!types.has(spec.type)) throw new Error(`argument --${spec.name} has an unknown type`);
    if (spec.short !== undefined) {
      if (spec.short.length !== 1 || shorts.has(spec.short))
        throw new Error(`argument --${spec.name} has an invalid or duplicate short name`);
      shorts.add(spec.short);
    }
    if (spec.enum?.length) {
      if (spec.type !== "string" && spec.type !== "[]string") throw new Error(`argument --${spec.name}: enum requires string or []string`);
      if (!spec.required && spec.default === undefined)
        throw new Error(`argument --${spec.name}: enum requires a default or required argument`);
    }
    if (spec.default !== undefined) validateValue(spec, spec.default);
  }
}

export function resolveArgs(
  specs: readonly ArgSpec[],
  args: Args,
  options: { allowUnknown?: boolean; allowMissing?: boolean } = {},
): Record<string, string> {
  validateArgSpecs(specs);
  const names = new Set(specs.map((spec) => spec.name));
  if (!options.allowUnknown)
    for (const name of Object.keys(args)) {
      if (!names.has(name)) throw new Error(`no flag --${name} is declared`);
    }
  const out: Record<string, string> = { ...args };
  for (const spec of specs) {
    const value = Object.hasOwn(args, spec.name) ? args[spec.name] : spec.default;
    if (value === undefined) {
      if (spec.required && !options.allowMissing) throw new Error(`argument --${spec.name} is required`);
    } else
      Object.defineProperty(out, spec.name, { value: validateValue(spec, value), enumerable: true, configurable: true, writable: true });
  }
  return out;
}
