import assert from "node:assert/strict";
import { test } from "node:test";
import { SDKError } from "../src/errors.ts";
import { definePipeline, resetPipelines } from "../src/plan.ts";
import { Host, hello } from "./helpers.ts";

test("planning diagnostics exclude secret arguments across scopes, encodings and error fields", async () => {
  let seed = 104;
  for (let n = 0; n < 8; n++) {
    seed = (seed * 1664525 + 1013904223) >>> 0;
    const secret = `private-${seed.toString(16)}-秘密-${[".*+?^${}()|[]", '\u0000"\\\n', "é", "value"][n % 4]}`;
    const variants = [
      secret,
      secret + "-tail",
      JSON.stringify(secret).slice(1, -1),
      encodeURIComponent(secret),
      Buffer.from(secret).toString("base64"),
    ];
    for (const scope of ["pipeline", "job", "generated"]) {
      for (const supplied of [false, true]) {
        for (const kind of ["canonical", "error", "string"]) {
          resetPipelines();
          const spec = { name: "token", type: "string" as const, secret: true, default: supplied ? "unused-default" : secret };
          const fail = () => {
            const message = `declaration refused ${variants.join(" ")}`;
            if (kind === "canonical")
              throw new SDKError(message, { code: "plan_failed", hint: message, details: { [secret]: { nested: variants } } });
            if (kind === "error") throw new Error(message);
            throw message;
          };
          definePipeline({
            name: "fixture",
            extra: scope === "generated",
            ...(scope === "pipeline" ? { args: [spec] } : {}),
            plan(plan) {
              const source = plan.job("build", () => null);
              if (scope === "generated") {
                source.generate((generated) => {
                  generated.job("child", () => null).withArgs(spec);
                  fail();
                });
              } else {
                if (scope === "job") source.withArgs(spec, { ...spec, name: "overlap", default: secret + "-tail" });
                fail();
              }
            },
          });
          for (const op of scope === "generated" || supplied ? ["plan"] : ["describe", "plan"]) {
            const host = new Host();
            try {
              assert.equal(
                (
                  await host.send({
                    ...hello,
                    binding: { run_id: "r", job_id: "build", routes: { url: "http://127.0.0.1:12345", bearer: "b" } },
                  })
                ).ok,
                true,
              );
              let reply = await host.send(
                op === "describe"
                  ? { id: "describe", op }
                  : {
                      id: "plan",
                      op,
                      pipeline: "fixture",
                      args: supplied ? { token: secret } : {},
                      run: { run_id: "r", pipeline: "fixture" },
                    },
              );
              if (scope === "generated") {
                assert.equal(reply.ok, true, JSON.stringify(reply));
                reply = await host.send({ id: "generate", op: "eval", closure: reply.result.jobs[0].closures.generate[0], input: null });
                reply = reply.result;
              }
              if (op === "describe") {
                assert.equal(reply.ok, true, JSON.stringify(reply));
                reply = reply.result.pipelines[0];
              }
              assert.ok(reply.error, JSON.stringify(reply));
              assert.match(reply.error.message, /declaration refused/);
              for (const value of variants)
                assert.equal(
                  JSON.stringify(reply.error).includes(JSON.stringify(value).slice(1, -1)),
                  false,
                  `${scope}/${supplied}/${kind}/${op}`,
                );
            } finally {
              await host.close();
            }
          }
        }
      }
    }
  }
});

test("argument resolution diagnostics mask supplied secrets even when used as invalid flag names", async () => {
  resetPipelines();
  const secret = "private-flag-name";
  definePipeline({ name: "fixture", args: [{ name: "token", type: "string", secret: true }], plan() {} });
  const host = new Host();
  try {
    await host.send(hello);
    const reply = await host.send({
      id: "plan",
      op: "plan",
      pipeline: "fixture",
      args: { token: secret, [secret]: "x" },
      run: { run_id: "r", pipeline: "fixture" },
    });
    assert.equal(reply.error.code, "invalid_args");
    assert.equal(JSON.stringify(reply.error).includes(secret), false);
  } finally {
    await host.close();
  }
});

test("planning diagnostic details mask primitive secret values", async () => {
  for (const secret of ["true", "null", "123456"]) {
    resetPipelines();
    definePipeline({
      name: "fixture",
      args: [{ name: "token", type: "string", secret: true, default: secret }],
      plan() {
        throw new SDKError("declaration refused", { code: "plan_failed", details: { value: JSON.parse(secret) } });
      },
    });
    const host = new Host();
    try {
      await host.send(hello);
      const reply = await host.send({ id: "plan", op: "plan", pipeline: "fixture", args: {}, run: { run_id: "r", pipeline: "fixture" } });
      assert.equal(reply.error.details.value, "***");
    } finally {
      await host.close();
    }
  }
});
