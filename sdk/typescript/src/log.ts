import type { Level, Logger } from "../index.d.ts";
import { writeFrame } from "./protocol.ts";
export interface LineSink {
  write(line: string): unknown;
}
export interface LogRecord {
  ts: string;
  level?: Level;
  step?: string;
  event?: string;
  msg?: string;
  attrs?: Record<string, unknown>;
  stream?: string;
  stream_data?: string;
  stream_end?: boolean;
}
export class LogWriter implements Logger {
  readonly #sink: LineSink;
  readonly #step: string | undefined;
  readonly #now: () => Date;
  constructor(sink: LineSink, step?: string, now: () => Date = () => new Date()) {
    this.#sink = sink;
    this.#step = step;
    this.#now = now;
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
  event(event: string, attrs?: Record<string, unknown>): void {
    if (event === "exec_chunk") {
      this.#emit({
        ts: this.#now().toISOString(), level: "info", event: "exec_line", stream: String(attrs?.stream ?? ""),
        ...(attrs?.data === undefined ? {} : { stream_data: String(attrs.data) }),
        ...(attrs?.end === true ? { stream_end: true } : {}),
      });
      return;
    }
    const msg =
      event === "exec_start" ? `$ ${String(attrs?.command ?? "")}` : undefined;
    this.#emit({ ts: this.#now().toISOString(), level: "info", event, ...(attrs ? { attrs } : {}), ...(msg === undefined ? {} : { msg }) });
  }
  #log(level: Level, msg: string, attrs?: Record<string, unknown>): void {
    this.#emit({ ts: this.#now().toISOString(), level, msg, ...(attrs ? { attrs } : {}) });
  }
  #emit(record: LogRecord): void {
    writeFrame(this.#sink, { ...record, ...(this.#step === undefined ? {} : { step: this.#step }) });
  }
}
