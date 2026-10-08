// Log records the SDK writes to stdout, one JSON object per line. The field
// names match the engine's LogRecord so the host forwards them unchanged.

export type Level = "debug" | "info" | "warn" | "error";

export interface LogRecord {
  ts: string;
  level?: Level;
  node?: string;
  step?: string;
  event?: string;
  msg?: string;
  attrs?: Record<string, unknown>;
}

export interface LineSink {
  write(line: string): unknown;
}

export interface LogScope {
  node?: string;
  step?: string;
}

/** Writes NDJSON log records scoped to one node and, optionally, one step. */
export class LogWriter {
  readonly #sink: LineSink;
  readonly #scope: LogScope;
  readonly #now: () => Date;

  constructor(sink: LineSink, scope: LogScope = {}, now: () => Date = () => new Date()) {
    this.#sink = sink;
    this.#scope = scope;
    this.#now = now;
  }

  /** Returns a writer whose records carry the given step id. */
  forStep(step: string): LogWriter {
    return new LogWriter(this.#sink, { ...this.#scope, step }, this.#now);
  }

  debug(msg: string, attrs?: Record<string, unknown>): void {
    this.#log("debug", msg, attrs);
  }

  info(msg: string, attrs?: Record<string, unknown>): void {
    this.#log("info", msg, attrs);
  }

  warn(msg: string, attrs?: Record<string, unknown>): void {
    this.#log("warn", msg, attrs);
  }

  error(msg: string, attrs?: Record<string, unknown>): void {
    this.#log("error", msg, attrs);
  }

  /** Writes a structured event record, such as exec_end with resource numbers. */
  event(event: string, attrs?: Record<string, unknown>): void {
    this.emit({ ts: this.#now().toISOString(), level: "info", event, ...(attrs ? { attrs } : {}) });
  }

  emit(rec: LogRecord): void {
    const scoped: LogRecord = { ...rec };
    if (this.#scope.node !== undefined && scoped.node === undefined) scoped.node = this.#scope.node;
    if (this.#scope.step !== undefined && scoped.step === undefined) scoped.step = this.#scope.step;
    this.#sink.write(JSON.stringify(scoped) + "\n");
  }

  #log(level: Level, msg: string, attrs?: Record<string, unknown>): void {
    this.emit({ ts: this.#now().toISOString(), level, msg, ...(attrs ? { attrs } : {}) });
  }
}
