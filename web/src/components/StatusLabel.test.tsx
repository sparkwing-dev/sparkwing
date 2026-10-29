import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import StatusLabel from "./StatusLabel";

describe("StatusLabel", () => {
  it("labels a completed preview as a dry run", () => {
    const html = renderToStaticMarkup(<StatusLabel status="dry_run" />);
    assert.match(html, />dry run<\/span>/);
    assert.match(html, /text-violet-400/);
    assert.doesNotMatch(html, /success/);
  });
});
