import type { JobSpec } from "./plan-wire.ts";
import { failure } from "./errors.ts";

export class StepState {
  readonly outputs = new Map<string, unknown>();
  readonly #terminal = new Set<string>();
  readonly #active = new Set<string>();
  #attempt = 0;

  begin(job: JobSpec, current: string, attempt: number, settled: readonly string[]): void {
    const invalid = (message: string): never => {
      throw failure("protocol_error", message);
    };
    if (
      !Number.isSafeInteger(attempt) ||
      attempt < 1 ||
      attempt < this.#attempt ||
      !Array.isArray(settled) ||
      (attempt !== this.#attempt && this.#active.size > 0) ||
      this.#active.has(current)
    )
      invalid("step state is absent, stale or overlaps another attempt");
    if (attempt === this.#attempt && this.#terminal.has(current)) invalid("step is already terminal in this attempt");
    const steps = new Map((job.steps ?? []).map((step) => [step.id, step]));
    const terminal = new Set<string>();
    for (const id of settled) {
      if (!steps.has(id) || id === current || terminal.has(id) || this.#active.has(id))
        invalid("settled steps contain an invalid or duplicate step");
      terminal.add(id);
    }
    const pending = [...(steps.get(current)?.needs ?? [])];
    const checked = new Set([current]);
    while (pending.length > 0) {
      const id = pending.pop()!;
      if (!terminal.has(id)) invalid("step prerequisite is not settled");
      if (!checked.has(id)) {
        checked.add(id);
        pending.push(...(steps.get(id)?.needs ?? []));
      }
    }
    if (attempt !== this.#attempt) {
      this.outputs.clear();
      this.#terminal.clear();
      this.#attempt = attempt;
    }
    for (const id of settled) this.#terminal.add(id);
    this.#active.add(current);
  }

  finish(step: string, terminal: boolean): void {
    if (terminal) this.#terminal.add(step);
    this.#active.delete(step);
  }
}
