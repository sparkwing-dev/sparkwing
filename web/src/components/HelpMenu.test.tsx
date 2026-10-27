import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import HelpMenu, { HelpPanel } from "./HelpMenu";
import { limitHelp, supportContact } from "@/lib/support";

describe("HelpMenu", () => {
  it("shows a Help button that starts closed", () => {
    const html = renderToStaticMarkup(<HelpMenu />);
    assert.match(html, />Help<\/button>/);
    assert.match(html, /aria-expanded="false"/);
    assert.doesNotMatch(html, /role="dialog"/);
  });

  it("opens a panel with the friendly message", () => {
    const html = renderToStaticMarkup(<HelpMenu initialOpen />);
    assert.match(html, /role="dialog"/);
    assert.match(html, /Need higher limits, have feedback/);
    assert.match(html, /we read everything/);
  });

  it("links the configured support email and the docs", () => {
    const html = renderToStaticMarkup(
      <HelpPanel contact={supportContact("support@example.com")} />,
    );
    assert.match(html, /href="mailto:support@example.com"/);
    assert.match(html, /href="https:\/\/sparkwing.dev\/docs\/"/);
    assert.doesNotMatch(html, /administrator/);
  });

  it("sends a deployment without a contact to its administrator", () => {
    const html = renderToStaticMarkup(<HelpPanel contact={null} />);
    assert.match(html, /Contact your Sparkwing administrator/);
    assert.match(html, /href="https:\/\/sparkwing.dev\/docs\/"/);
    assert.doesNotMatch(html, /mailto:/);
  });
});

describe("supportContact", () => {
  it("accepts an email or an http(s) URL and nothing else", () => {
    assert.deepEqual(supportContact("https://example.com/help"), {
      label: "https://example.com/help",
      href: "https://example.com/help",
    });
    assert.equal(supportContact("")?.href, undefined);
    assert.equal(supportContact("javascript:alert(1)"), null);
    assert.equal(supportContact("not a contact"), null);
  });

  it("names the remedy in limit refusals", () => {
    assert.equal(
      limitHelp(supportContact("support@example.com")),
      "Contact support@example.com to raise this limit.",
    );
    assert.equal(
      limitHelp(null),
      "Contact your Sparkwing administrator to raise this limit.",
    );
  });
});
