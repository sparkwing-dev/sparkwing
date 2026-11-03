import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import RunRequester from "./RunRequester";

describe("RunRequester", () => {
  it("names the agent beside the requester with an accessible label", () => {
    const html = renderToStaticMarkup(
      <RunRequester requester="korey" agent="claude-code:land-agent" />,
    );
    assert.match(html, /by korey/);
    assert.match(html, /aria-label="started by an agent: claude-code:land-agent"/);
    assert.match(html, /<svg/);
    assert.match(html, />claude-code:land-agent</);
  });

  it("shows no agent icon when no agent started the run", () => {
    const html = renderToStaticMarkup(<RunRequester requester="korey" />);
    assert.match(html, /by korey/);
    assert.doesNotMatch(html, /agent/);
    assert.doesNotMatch(html, /<svg/);
  });

  it("renders nothing without a requester or agent", () => {
    assert.equal(renderToStaticMarkup(<RunRequester />), "");
  });
});
