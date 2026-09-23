import assert from "node:assert/strict";
import { afterEach, before, beforeEach, describe, it } from "node:test";

type GitCredentials = typeof import("./gitCredentials");
let lib!: GitCredentials;

type Runtime = {
  window?: { __SPARKWING_REQUIRE_LOGIN__?: string };
  document?: { cookie: string };
  fetch: typeof fetch;
};
const runtime = globalThis as unknown as Runtime;

before(async () => {
  runtime.window = {};
  try {
    lib = await import("./gitCredentials");
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

const cred = (
  over: Partial<import("./gitCredentials").GitCredential>,
): import("./gitCredentials").GitCredential => ({
  host: "gitlab.com",
  kind: "ssh",
  confirmed: false,
  created_by: "alice",
  created_at: 1,
  updated_at: 2,
  ...over,
});

const key =
  "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----\n";

describe("hostProblem", () => {
  it("accepts hostnames the controller accepts", () => {
    for (const ok of ["gitlab.com", "Git.Example.org ", "a-b.c1.io"]) {
      assert.equal(lib.hostProblem(ok), null, ok);
    }
  });

  it("refuses a scheme, port, path or bare name", () => {
    for (const bad of [
      "",
      "https://gitlab.com",
      "gitlab.com:22",
      "gitlab.com/acme",
      "git@gitlab.com",
      "localhost",
      "-a.com",
      ".a.com",
      "a.com.",
      "a_b.com",
      `${"a".repeat(250)}.com`,
    ]) {
      assert.notEqual(lib.hostProblem(bad), null, bad);
    }
  });
});

describe("draftProblem", () => {
  const ssh = { ...blankDraft(), host: "gitlab.com", privateKey: key };
  const https = {
    ...blankDraft(),
    host: "bitbucket.org",
    kind: "https" as const,
    token: "tok",
  };

  it("needs a private key for ssh and checks the port", () => {
    assert.equal(lib.draftProblem(ssh), null);
    assert.match(
      lib.draftProblem({ ...ssh, privateKey: "ssh-ed25519 AAAA" }) ?? "",
      /private key/,
    );
    for (const port of ["0", "65536", "22a", "-1"]) {
      assert.notEqual(lib.draftProblem({ ...ssh, port }), null, port);
    }
    assert.equal(lib.draftProblem({ ...ssh, port: "2222" }), null);
  });

  it("needs a printable token and username for https", () => {
    assert.equal(lib.draftProblem(https), null);
    assert.notEqual(lib.draftProblem({ ...https, token: "" }), null);
    assert.notEqual(lib.draftProblem({ ...https, token: "a b" }), null);
    assert.notEqual(
      lib.draftProblem({ ...https, token: "x".repeat(1025) }),
      null,
    );
    assert.notEqual(lib.draftProblem({ ...https, username: "a b" }), null);
    assert.equal(
      lib.draftProblem({ ...https, username: " x-token-auth " }),
      null,
    );
  });

  it("reports a bad host first", () => {
    assert.match(
      lib.draftProblem({ ...ssh, host: "https://x.com" }) ?? "",
      /no scheme/,
    );
  });
});

function blankDraft() {
  return {
    host: "",
    kind: "ssh" as const,
    privateKey: "",
    port: "",
    token: "",
    username: "",
  };
}

describe("draftBody", () => {
  it("sends ssh with the key and an optional numeric port", () => {
    assert.deepEqual(
      lib.draftBody({
        ...blankDraft(),
        host: " GitLab.com ",
        privateKey: key,
        token: "ignored",
      }),
      { host: "gitlab.com", kind: "ssh", private_key: key },
    );
    assert.deepEqual(
      lib.draftBody({
        ...blankDraft(),
        host: "git.acme.io",
        privateKey: key,
        port: " 2222 ",
      }),
      { host: "git.acme.io", kind: "ssh", private_key: key, port: 2222 },
    );
  });

  it("sends https with the token and only a named username", () => {
    const base = {
      ...blankDraft(),
      host: "bitbucket.org",
      kind: "https" as const,
      token: "tok",
      privateKey: "ignored",
    };
    assert.deepEqual(lib.draftBody(base), {
      host: "bitbucket.org",
      kind: "https",
      token: "tok",
    });
    assert.deepEqual(lib.draftBody({ ...base, username: " x-token-auth " }), {
      host: "bitbucket.org",
      kind: "https",
      token: "tok",
      username: "x-token-auth",
    });
  });
});

describe("rows", () => {
  it("reads a non-default port back from the known_hosts line", () => {
    assert.equal(
      lib.portFromHostKey("[git.acme.io]:2222 ssh-ed25519 AAAA"),
      "2222",
    );
    assert.equal(lib.portFromHostKey("gitlab.com ssh-ed25519 AAAA"), "");
    assert.equal(lib.portFromHostKey(undefined), "");
  });

  it("opens an update with the row's host, kind and username and no secret", () => {
    assert.deepEqual(
      lib.updateDraft(
        cred({ host: "git.acme.io", host_key: "[git.acme.io]:2222 ssh-rsa A" }),
      ),
      { ...blankDraft(), host: "git.acme.io", port: "2222" },
    );
    assert.deepEqual(
      lib.updateDraft(
        cred({
          host: "bitbucket.org",
          kind: "https",
          username: "x-token-auth",
        }),
      ),
      {
        ...blankDraft(),
        host: "bitbucket.org",
        kind: "https",
        username: "x-token-auth",
      },
    );
  });

  it("asks for confirmation only of an unconfirmed ssh key", () => {
    assert.equal(lib.needsConfirmation(cred({})), true);
    assert.equal(lib.needsConfirmation(cred({ confirmed: true })), false);
    assert.equal(
      lib.needsConfirmation(cred({ kind: "https", confirmed: false })),
      false,
    );
    assert.equal(lib.statusLabel(cred({})), "Host key not confirmed");
    assert.equal(
      lib.statusLabel(cred({ confirmed: true })),
      "Host key confirmed",
    );
    assert.equal(lib.statusLabel(cred({ kind: "https" })), "Ready");
  });

  it("sorts by host and finds the row a host would replace", () => {
    const rows = lib.sortCredentials([
      cred({ host: "z.io" }),
      cred({ host: "a.io" }),
    ]);
    assert.deepEqual(
      rows.map((r) => r.host),
      ["a.io", "z.io"],
    );
    assert.equal(lib.existingCredential(rows, " Z.io ")?.host, "z.io");
    assert.equal(lib.existingCredential(rows, "b.io"), undefined);
  });

  it("recognises github.com only", () => {
    assert.equal(lib.isGitHubHost(" GitHub.com "), true);
    assert.equal(lib.isGitHubHost("github.example.com"), false);
  });
});

describe("API helpers", () => {
  it("lists credentials from the envelope", async () => {
    respond = () =>
      new Response(JSON.stringify({ credentials: [cred({})] }), {
        status: 200,
      });
    const got = await lib.listGitCredentials();
    assert.deepEqual(
      got.map((c) => c.host),
      ["gitlab.com"],
    );
    assert.equal(calls[0].url, "/api/v1/team/git-credentials");
    assert.equal(calls[0].method, "GET");
  });

  it("posts a credential and returns the public key", async () => {
    respond = () =>
      new Response(
        JSON.stringify({ ...cred({}), public_key: "ssh-ed25519 AAAA" }),
        { status: 200 },
      );
    const got = await lib.putGitCredential({
      host: "gitlab.com",
      kind: "ssh",
      private_key: key,
    });
    assert.equal(got.public_key, "ssh-ed25519 AAAA");
    assert.equal(calls[0].method, "POST");
    assert.deepEqual(JSON.parse(calls[0].body), {
      host: "gitlab.com",
      kind: "ssh",
      private_key: key,
    });
  });

  it("confirms with the displayed fingerprint and escapes the host", async () => {
    respond = () =>
      new Response(JSON.stringify(cred({ confirmed: true })), { status: 200 });
    await lib.confirmGitCredential("gitlab.com", "SHA256:abc");
    await lib.deleteGitCredential("gitlab.com");
    assert.deepEqual(
      calls.map((c) => `${c.method} ${c.url}`),
      [
        "POST /api/v1/team/git-credentials/gitlab.com/confirm",
        "DELETE /api/v1/team/git-credentials/gitlab.com",
      ],
    );
    assert.deepEqual(JSON.parse(calls[0].body), { fingerprint: "SHA256:abc" });
  });

  it("lists releases", async () => {
    const release = {
      host: "gitlab.com",
      run_id: "r1",
      runner: "cloud",
      token_prefix: "",
      released_at: 3,
    };
    respond = () =>
      new Response(JSON.stringify({ releases: [release] }), { status: 200 });
    assert.deepEqual(await lib.listGitCredentialReleases(), [release]);
    assert.equal(calls[0].url, "/api/v1/team/git-credentials/releases");
  });

  it("opts a machine in with PUT", async () => {
    respond = () =>
      new Response(JSON.stringify({ enabled: true }), { status: 200 });
    assert.equal(await lib.setMachineGitCredentials("swr_ab/c", true), true);
    assert.equal(calls[0].method, "PUT");
    assert.equal(
      calls[0].url,
      "/api/v1/team/runner-tokens/swr_ab%2Fc/git-credentials",
    );
    assert.deepEqual(JSON.parse(calls[0].body), { enabled: true });
  });

  it("surfaces the controller's refusal", async () => {
    respond = () =>
      new Response(
        JSON.stringify({
          error: "secrets_key_required",
          message: "the controller has no secrets key",
        }),
        { status: 409 },
      );
    await assert.rejects(
      lib.putGitCredential({ host: "gitlab.com", kind: "https", token: "t" }),
      /Save the credential for gitlab.com: the controller has no secrets key/,
    );
  });
});
