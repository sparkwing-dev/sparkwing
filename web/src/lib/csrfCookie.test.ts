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
  it("reads the prefixed cookie when a bare cookie is also present", () => {
    withCookies("sw_csrf=planted; theme=dark; __Host-sw_csrf=real");
    assert.equal(readCSRFCookie(), "real");
  });

  it("ignores the removed bare cookie name", () => {
    withCookies("theme=dark; sw_csrf=local");
    assert.equal(readCSRFCookie(), "");
  });

  it("reports no token when the prefixed name is absent", () => {
    withCookies("theme=dark");
    assert.equal(readCSRFCookie(), "");
  });

  it("reports no token when the value will not decode", () => {
    withCookies("__Host-sw_csrf=%E0%A4%A");
    assert.equal(readCSRFCookie(), "");
  });
});
