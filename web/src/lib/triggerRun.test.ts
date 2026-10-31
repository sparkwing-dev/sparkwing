import assert from "node:assert/strict";
import { before, describe, it } from "node:test";

let triggerRun!: typeof import("./api").triggerRun;

before(async () => {
  ({ triggerRun } = await import("./api"));
});

describe("run submission refusals", () => {
  async function refusal(body: string): Promise<Error> {
    const previousFetch = globalThis.fetch;
    globalThis.fetch = async () => new Response(body, { status: 403 });
    try {
      await triggerRun("build");
      assert.fail("submission should fail");
    } catch (error) {
      assert.ok(error instanceof Error);
      return error;
    } finally {
      globalThis.fetch = previousFetch;
    }
  }

  it("explains how to grant a repository that the GitHub App cannot access", async () => {
    const error = await refusal(JSON.stringify({ error: {
      code: "forbidden", message: "repository is not authorized",
      details: { reason: "repository_unauthorized" },
    } }));
    assert.match(error.message, /does not have access to this repository/i);
    assert.match(error.message, /connect or grant the repository under Team > GitHub/i);
    assert.doesNotMatch(error.message, /repository_unauthorized|"error"/);
  });

  it("shows a structured refusal's explanation and remedy without its JSON", async () => {
    const error = await refusal(JSON.stringify({ error: {
      code: "forbidden", message: "Your role cannot start runs.",
      hint: "Ask a team owner for editor access.",
    } }));
    assert.equal(error.message, "Your role cannot start runs. Ask a team owner for editor access.");
  });

  it("uses a friendly fallback for unexpected error bodies", async () => {
    for (const body of ["", "<html>proxy failure</html>", '{"error":{"code":"new_code"}}']) {
      const error = await refusal(body);
      assert.match(error.message, /could not start the run/i);
      assert.doesNotMatch(error.message, /html|new_code|\{/);
    }
  });
});
