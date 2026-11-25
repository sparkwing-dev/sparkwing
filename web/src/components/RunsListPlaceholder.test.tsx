import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import RunsListPlaceholder from "./RunsListPlaceholder";

describe("RunsListPlaceholder", () => {
  it("shows loading rows, not the empty text, before the first response", () => {
    const html = renderToStaticMarkup(
      <RunsListPlaceholder loading failed={false} emptyText="No runs yet" />,
    );
    assert.match(html, /role="status"[^>]*aria-label="Loading runs"/);
    assert.doesNotMatch(html, /No runs yet/);
  });

  it("shows the empty text once an empty response has arrived", () => {
    const html = renderToStaticMarkup(
      <RunsListPlaceholder loading={false} failed={false} emptyText="No runs yet" />,
    );
    assert.match(html, /No runs yet/);
    assert.doesNotMatch(html, /Loading runs/);
  });

  it("leaves the space to the error when the request failed", () => {
    for (const loading of [true, false]) {
      const html = renderToStaticMarkup(
        <RunsListPlaceholder loading={loading} failed emptyText="No runs yet" />,
      );
      assert.equal(html, "");
    }
  });
});
