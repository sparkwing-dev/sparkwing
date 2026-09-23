import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import { ExtraReposForm, ExtraReposTable } from "./GitHubAppExtraRepos";

const noop = () => {};
const list = { repository: "octo-org/api", extra_repos: ["octo-org/lib"] };

describe("ExtraReposTable", () => {
  it("shows each list, with changes only for an owner", () => {
    const owner = renderToStaticMarkup(
      <ExtraReposTable
        lists={[list]}
        canManage
        busy={false}
        onEdit={noop}
        onClear={noop}
      />,
    );
    assert.match(owner, /octo-org\/api/);
    assert.match(owner, /octo-org\/lib/);
    assert.match(owner, />Clear</);
    const reader = renderToStaticMarkup(
      <ExtraReposTable
        lists={[list]}
        canManage={false}
        busy={false}
        onEdit={noop}
        onClear={noop}
      />,
    );
    assert.doesNotMatch(reader, />Clear</);
    assert.doesNotMatch(reader, />Edit</);
  });

  it("says a token reads only its repository when nothing is listed", () => {
    const html = renderToStaticMarkup(
      <ExtraReposTable
        lists={[]}
        canManage
        busy={false}
        onEdit={noop}
        onClear={noop}
      />,
    );
    assert.match(html, /reads only its own repository/);
  });
});

describe("ExtraReposForm", () => {
  it("offers only covered repositories of the source's owner", () => {
    const html = renderToStaticMarkup(
      <ExtraReposForm
        repositories={["octo-org/api", "octo-org/lib", "other/lib"]}
        initial={list}
        busy={false}
        onSave={noop}
      />,
    );
    assert.match(html, /<input type="checkbox" checked=""\/>octo-org\/lib/);
    assert.doesNotMatch(html, /type="checkbox"[^>]*\/>other\/lib/);
  });
});
