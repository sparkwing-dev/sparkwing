import assert from "node:assert/strict";
import { afterEach, before, beforeEach, describe, it } from "node:test";

type SignInsLib = typeof import("./signIns");
let lib!: SignInsLib;

type Runtime = {
  window?: { __SPARKWING_REQUIRE_LOGIN__?: string };
  document?: { cookie: string };
  fetch: typeof fetch;
};
const runtime = globalThis as unknown as Runtime;

before(async () => {
  runtime.window = {};
  try {
    lib = await import("./signIns");
  } finally {
    delete runtime.window;
  }
});

type Call = { url: string; method: string; headers: Headers };
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
      headers: new Headers(init?.headers),
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

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("linkNotice", () => {
  it("says nothing without a result", () => {
    assert.equal(lib.linkNotice(""), null);
    assert.equal(lib.linkNotice("?other=1"), null);
  });

  it("confirms a link by the provider's name", () => {
    assert.deepEqual(lib.linkNotice("?linked=github"), {
      tone: "success",
      message: "GitHub is now a way to sign in to this account.",
      explain: false,
    });
  });

  it("explains a sign-in already on another account and points at the guidance", () => {
    const notice = lib.linkNotice(
      "?refused=identity_linked_elsewhere&provider=github",
    );
    assert.equal(notice?.tone, "error");
    assert.equal(notice?.explain, true);
    assert.match(
      notice?.message ?? "",
      /That GitHub sign-in is already linked to another Sparkwing account, so nothing changed\./,
    );
  });

  it("words every refusal itself and never echoes the URL", () => {
    for (const code of [
      "identity_already_linked",
      "provider_already_linked",
      "reauth_required",
      "rate_limited",
      "link_state_invalid",
      "provider_unverified",
      "provider_rejected",
      "provider_unreachable",
      "<script>",
    ]) {
      const notice = lib.linkNotice(
        `?refused=${encodeURIComponent(code)}&provider=google`,
      );
      assert.equal(notice?.tone, "error", code);
      assert.equal(notice?.explain, false, code);
      assert.doesNotMatch(notice?.message ?? "", /<script>/, code);
    }
    assert.equal(
      lib.linkNotice("?linked=%3Cb%3Eevil%3C%2Fb%3E"),
      null,
      "an unknown provider is not confirmed",
    );
    assert.equal(
      lib.linkNotice("?refused=link_failed&provider=%3Cb%3E")?.message,
      "That sign-in wasn't linked. Try again.",
    );
  });
});

describe("signInRows", () => {
  it("lists every offered provider and keeps a linked one no longer offered", () => {
    const rows = lib.signInRows({
      providers: ["google", "github"],
      identities: [
        { provider: "github", email: "octo@example.com", created_at: 1 },
        { provider: "gitlab", email: "", created_at: 2 },
      ],
    });
    assert.deepEqual(
      rows.map((r) => [r.provider, r.linked?.email ?? null]),
      [
        ["google", null],
        ["github", "octo@example.com"],
        ["gitlab", ""],
      ],
    );
  });
});

describe("unlinkSignIn", () => {
  it("sends a DELETE with the CSRF header", async () => {
    assert.deepEqual(await lib.unlinkSignIn("github"), { kind: "unlinked" });
    assert.equal(calls.length, 1);
    assert.equal(calls[0].method, "DELETE");
    assert.equal(calls[0].url, "/api/v1/me/identities/github");
    assert.equal(calls[0].headers.get("X-CSRF-Token"), "session-csrf");
  });

  it("reports the last method and a stale sign-in as answers", async () => {
    respond = () =>
      json(409, { error: "last_sign_in_method", message: "only one" });
    assert.deepEqual(await lib.unlinkSignIn("google"), { kind: "last" });
    respond = () => json(403, { error: "reauth_required", message: "old" });
    assert.deepEqual(await lib.unlinkSignIn("google"), { kind: "reauth" });
  });

  it("throws any other failure", async () => {
    respond = () => json(500, { error: "internal server error" });
    await assert.rejects(lib.unlinkSignIn("google"), /Unlink Google/);
  });
});
