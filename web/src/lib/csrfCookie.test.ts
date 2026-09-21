import assert from "node:assert/strict";
import { afterEach, describe, it } from "node:test";
import { readCSRFCookie } from "./csrfCookie";

type RuntimeDocument = { cookie: string };

const runtime = globalThis as unknown as { document?: RuntimeDocument };
const hadDocument = Object.prototype.hasOwnProperty.call(runtime, "document");
const previousDocument = runtime.document;

function withCookies(cookie: string) {
  runtime.document = { cookie };
}

afterEach(() => {
  if (hadDocument) runtime.document = previousDocument;
  else delete runtime.document;
});

describe("readCSRFCookie", () => {
  it("prefers the prefixed cookie a sibling host cannot write", () => {
    withCookies("sw_csrf=planted; theme=dark; __Host-sw_csrf=real");
    assert.equal(readCSRFCookie(), "real");
  });

  it("falls back to the bare name the insecure escape sets", () => {
    withCookies("theme=dark; sw_csrf=local");
    assert.equal(readCSRFCookie(), "local");
  });

  it("reports no token when neither name is present", () => {
    withCookies("theme=dark");
    assert.equal(readCSRFCookie(), "");
  });

  it("reports no token when the value will not decode", () => {
    withCookies("__Host-sw_csrf=%E0%A4%A");
    assert.equal(readCSRFCookie(), "");
  });
});
