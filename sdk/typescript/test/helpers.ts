import { PassThrough } from "node:stream";
import { serve, type ServeOptions } from "../src/runner.ts";

export class Host {
  input = new PassThrough();
  frames: Record<string, any>[] = [];
  waiters = new Map<string, (frame: Record<string, any>) => void>();
  done: Promise<void>;
  timers = new Set<NodeJS.Timeout>();
  constructor(options: Omit<ServeOptions, "input" | "output"> = {}) {
    this.done = serve({
      input: this.input,
      output: {
        write: (line: string) => {
          const frame = JSON.parse(line);
          this.frames.push(frame);
          this.waiters.get(frame.reply)?.(frame);
        },
      },
      ...options,
    });
  }
  send(request: Record<string, unknown>): Promise<Record<string, any>> {
    return new Promise((resolve, reject) => {
      const id = String(request.id);
      const timer = setTimeout(() => {
        this.waiters.delete(id);
        reject(new Error(`host timed out waiting for ${id}`));
      }, 10000);
      this.timers.add(timer);
      this.waiters.set(id, (frame) => {
        clearTimeout(timer);
        this.timers.delete(timer);
        this.waiters.delete(id);
        resolve(frame);
      });
      this.input.write(JSON.stringify(request) + "\n");
    });
  }
  async close() {
    this.input.end();
    await this.done;
    for (const timer of this.timers) clearTimeout(timer);
    this.timers.clear();
    this.waiters.clear();
  }
}
export const hello = {
  id: "hello",
  op: "hello",
  protocol: 2,
  engine: { version: "v0.71.0" },
  runner: { name: "granted", type: "runner", labels: ["os=linux"] },
};
