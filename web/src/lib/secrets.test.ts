import assert from "node:assert/strict";
import { afterEach, before, beforeEach, describe, it } from "node:test";

type Secrets = typeof import("./secrets");
let secrets!: Secrets;

type Runtime = {
  window?: { __SPARKWING_REQUIRE_LOGIN__?: string };
  document?: { cookie: string };
  fetch: typeof fetch;
};
const runtime = globalThis as unknown as Runtime;

before(async () => {
  runtime.window = {};
  try {
    secrets = await import("./secrets");
  } finally {
    delete runtime.window;
  }
});

type Call = { url: string; method: string; body: string };
let calls: Call[] = [];
let respond: (call: Call) => Response;
const realFetch = runtime.fetch;

beforeEach(() => {
  calls = [];
  respond = () => new Response(null, { status: 204 });
  runtime.window = { __SPARKWING_REQUIRE_LOGIN__: "true" };
  runtime.document = { cookie: "__Host-sw_csrf=session-csrf" };
  runtime.fetch = async (input, init) => {
    const call = {
      url: String(input),
      method: init?.method ?? "GET",
      body: typeof init?.body === "string" ? init.body : "",
    };
    calls.push(call);
    return respond(call);
  };
});

afterEach(() => {
  delete runtime.window;
  delete runtime.document;
  runtime.fetch = realFetch;
});

const row = (over: Partial<import("./secrets").StoredSecret>) => ({
  name: "X",
  principal: "alice",
  masked: true,
  bound: true,
  created_at: 1,
  updated_at: 2,
  ...over,
});

describe("secretNameProblem", () => {
  it("accepts the controller's grammar", () => {
    for (const ok of ["API_KEY", "a", "team/deploy.key", "x-1_y"]) {
      assert.equal(secrets.secretNameProblem(ok), null, ok);
    }
  });

  it("refuses what the controller refuses", () => {
    for (const bad of [
      "",
      "has space",
      "emoji✓",
      "a..b",
      "a//b",
      ".lead",
      "/lead",
      "-lead",
      "trail.",
      "trail/",
      "trail-",
      "x".repeat(257),
    ]) {
      assert.notEqual(secrets.secretNameProblem(bad), null, bad);
    }
    assert.equal(secrets.secretNameProblem("x".repeat(256)), null);
  });
});

describe("draftProblem", () => {
  const base = {
    name: "API_KEY",
    value: "v",
    masked: true,
    scope: "team" as const,
    pipeline: "",
  };

  it("needs a pipeline name when scoped to a pipeline", () => {
    assert.notEqual(
      secrets.draftProblem({ ...base, scope: "pipeline", pipeline: "  " }),
      null,
    );
    assert.equal(
      secrets.draftProblem({ ...base, scope: "pipeline", pipeline: "deploy" }),
      null,
    );
  });

  it("needs a value for a secret but lets a variable be empty", () => {
    assert.notEqual(secrets.draftProblem({ ...base, value: "" }), null);
    assert.equal(
      secrets.draftProblem({ ...base, value: "", masked: false }),
      null,
    );
  });

  it("reports a bad name first", () => {
    assert.match(
      secrets.draftProblem({ ...base, name: "a b" }) ?? "",
      /letters, digits/,
    );
  });
});

describe("write bodies", () => {
  it("sends a team-wide row as shared and unscoped", () => {
    assert.deepEqual(
      secrets.draftBody({
        name: "REGION",
        value: "us-west-2",
        masked: false,
        scope: "team",
        pipeline: "ignored",
      }),
      { name: "REGION", value: "us-west-2", masked: false, shared: true },
    );
  });

  it("sends a pipeline row scoped and unshared, keeping multi-line values", () => {
    assert.deepEqual(
      secrets.draftBody({
        name: "KEY",
        value: "line1\nline2\n",
        masked: true,
        scope: "pipeline",
        pipeline: " deploy-web ",
      }),
      {
        name: "KEY",
        value: "line1\nline2\n",
        masked: true,
        pipeline: "deploy-web",
      },
    );
  });

  it("keeps an updated row's scope, sharing and kind", () => {
    assert.deepEqual(
      secrets.updateBody(row({ name: "A", pipeline: "p", masked: true }), "n"),
      { name: "A", value: "n", masked: true, pipeline: "p" },
    );
    assert.deepEqual(
      secrets.updateBody(row({ name: "B", shared: true, masked: false }), "n"),
      { name: "B", value: "n", masked: false, shared: true },
    );
    assert.deepEqual(secrets.updateBody(row({ name: "C" }), "n"), {
      name: "C",
      value: "n",
      masked: true,
    });
  });
});

describe("listing", () => {
  it("splits secrets from variables, sorted by name then pipeline", () => {
    const { secrets: s, variables: v } = secrets.splitSecrets([
      row({ name: "B" }),
      row({ name: "A", pipeline: "z" }),
      row({ name: "A", pipeline: "a" }),
      row({ name: "R", masked: false, value: "x" }),
    ]);
    assert.deepEqual(
      s.map((r) => `${r.name}/${r.pipeline ?? ""}`),
      ["A/a", "A/z", "B/"],
    );
    assert.deepEqual(
      v.map((r) => r.name),
      ["R"],
    );
  });

  it("labels each scope", () => {
    assert.equal(
      secrets.scopeLabel({ pipeline: "deploy" }),
      "Pipeline: deploy",
    );
    assert.equal(secrets.scopeLabel({ shared: true }), "Team");
    assert.match(secrets.scopeLabel({}), /No runs/);
  });

  it("finds the row a draft would replace", () => {
    const rows = [row({ name: "A" }), row({ name: "A", pipeline: "p" })];
    const draft = {
      name: "A",
      value: "",
      masked: true,
      scope: "pipeline" as const,
      pipeline: "p",
    };
    assert.equal(secrets.existingRow(rows, draft)?.pipeline, "p");
    assert.equal(
      secrets.existingRow(rows, { ...draft, scope: "team" })?.pipeline,
      undefined,
    );
    assert.equal(secrets.existingRow(rows, { ...draft, name: "B" }), undefined);
  });
});

describe("API helpers", () => {
  it("lists rows from the secrets envelope", async () => {
    respond = () =>
      new Response(JSON.stringify({ secrets: [row({ name: "A" })] }), {
        status: 200,
      });
    const got = await secrets.listSecrets();
    assert.deepEqual(
      got.map((r) => r.name),
      ["A"],
    );
    assert.equal(calls[0].url, "/api/v1/secrets");
    assert.equal(calls[0].method, "GET");
  });

  it("posts a write with an explicit masked flag", async () => {
    await secrets.writeSecret({
      name: "R",
      value: "v",
      masked: false,
      shared: true,
    });
    assert.equal(calls[0].method, "POST");
    assert.equal(calls[0].url, "/api/v1/secrets");
    assert.deepEqual(JSON.parse(calls[0].body), {
      name: "R",
      value: "v",
      masked: false,
      shared: true,
    });
  });

  it("escapes the name and names the pipeline on delete", async () => {
    await secrets.deleteSecret({ name: "team/key", pipeline: "deploy web" });
    await secrets.deleteSecret({ name: "TEAM_KEY" });
    assert.deepEqual(
      calls.map((c) => `${c.method} ${c.url}`),
      [
        "DELETE /api/v1/secrets/team%2Fkey?pipeline=deploy%20web",
        "DELETE /api/v1/secrets/TEAM_KEY",
      ],
    );
  });

  it("surfaces the controller's refusal", async () => {
    respond = () =>
      new Response(
        JSON.stringify({
          error: "missing_scope",
          message: "token lacks required scope: admin",
        }),
        { status: 403 },
      );
    await assert.rejects(
      secrets.writeSecret({
        name: "R",
        value: "v",
        masked: true,
        shared: true,
      }),
      /Save R: token lacks required scope/,
    );
  });
});
