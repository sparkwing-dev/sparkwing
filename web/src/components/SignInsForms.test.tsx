import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import { HowAccountsWork, LinkNoticeBox, SignInRow } from "./SignInsForms";

const noop = () => {};

function text(html: string): string {
  return html
    .replace(/<[^>]+>/g, " ")
    .replace(/&#x27;|&apos;/g, "'")
    .replace(/\s+/g, " ");
}

describe("HowAccountsWork", () => {
  const page = text(renderToStaticMarkup(<HowAccountsWork />));

  it("says what an account holds and what belongs to teams", () => {
    assert.match(
      page,
      /Your account holds your sign-in methods and your team memberships\./,
    );
    assert.match(
      page,
      /Runs, secrets, credits and machines belong to teams, not to accounts\./,
    );
  });

  it("says linking adds a sign-in and moves nothing", () => {
    assert.match(
      page,
      /Linking adds another way to sign in to this account\. It doesn't bring anything over from another Sparkwing account\./,
    );
  });

  it("offers the two ways to deal with a second account", () => {
    assert.match(page, /Keep both\. From the other account, invite this one/);
    assert.match(page, /switch between the teams with the team switcher/);
    assert.match(
      page,
      /Delete the other one\. .* then link its sign-in here\./,
    );
    assert.match(page, /We don't currently offer combining accounts\./);
  });

  it("is the anchor a refusal links to", () => {
    assert.match(
      renderToStaticMarkup(<HowAccountsWork />),
      /<section id="how-accounts-work"/,
    );
  });
});

describe("LinkNoticeBox", () => {
  it("gives a refusal about another account the same guidance and a link", () => {
    const html = renderToStaticMarkup(
      <LinkNoticeBox
        notice={{
          tone: "error",
          message: "That GitHub sign-in is already linked to another Sparkwing account, so nothing changed.",
          explain: true,
        }}
      />,
    );
    const said = text(html);
    assert.match(html, /role="alert"/);
    assert.match(said, /already linked to another Sparkwing account/);
    assert.match(said, /Keep both\./);
    assert.match(said, /Delete the other one\./);
    assert.match(said, /We don't currently offer combining accounts\./);
    assert.match(html, /<a href="#how-accounts-work"[^>]*>How accounts work<\/a>/);
  });

  it("shows other results without the guidance", () => {
    const said = text(
      renderToStaticMarkup(
        <LinkNoticeBox
          notice={{ tone: "success", message: "GitHub is now a way to sign in to this account.", explain: false }}
        />,
      ),
    );
    assert.doesNotMatch(said, /Keep both/);
  });
});

describe("SignInRow", () => {
  it("links an unlinked provider through the dashboard server with the CSRF token", () => {
    const html = renderToStaticMarkup(
      <SignInRow
        provider="github"
        linked={null}
        onlyMethod={false}
        csrfToken="session-csrf"
        busy={false}
        onUnlink={noop}
      />,
    );
    const form = html.match(/<form[^>]*>/)?.[0] ?? "";
    assert.match(form, / action="\/auth\/github\/link"/);
    assert.match(form, / method="POST"/);
    assert.match(html, /<input type="hidden" name="csrf_token" value="session-csrf"\/>/);
    assert.match(text(html), /GitHub Not linked Link GitHub/);
  });

  it("shows a linked provider's address and refuses to unlink the only method", () => {
    const only = renderToStaticMarkup(
      <SignInRow
        provider="google"
        linked={{ provider: "google", email: "ada@example.com", created_at: 1 }}
        onlyMethod
        csrfToken="session-csrf"
        busy={false}
        onUnlink={noop}
      />,
    );
    assert.match(text(only), /Google Linked · ada@example\.com Unlink/);
    assert.match(only, /<button type="button"[^>]* disabled=""/);
    assert.match(only, /This is your only way to sign in/);

    const another = renderToStaticMarkup(
      <SignInRow
        provider="google"
        linked={{ provider: "google", email: "ada@example.com", created_at: 1 }}
        onlyMethod={false}
        csrfToken="session-csrf"
        busy={false}
        onUnlink={noop}
      />,
    );
    assert.doesNotMatch(another, /disabled=""/);
  });
});
