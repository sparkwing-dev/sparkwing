import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
  branchProblem,
  parseGitHubRepository,
  triggerGit,
} from "./triggerSource";

describe("parseGitHubRepository", () => {
  it("reads the forms a person pastes", () => {
    for (const input of [
      "https://github.com/acme/app",
      "https://github.com/acme/app/",
      "https://github.com/acme/app.git",
      "github.com/acme/app",
      "  HTTPS://GitHub.com/acme/app  ",
    ]) {
      const got = parseGitHubRepository(input);
      assert.deepEqual(
        got,
        {
          ok: true,
          value: {
            url: "https://github.com/acme/app",
            owner: "acme",
            name: "app",
          },
        },
        input,
      );
    }
  });

  it("refuses what the runner cannot fetch from GitHub", () => {
    for (const input of [
      "",
      "acme/app",
      "https://gitlab.com/acme/app",
      "http://github.com/acme/app",
      "git@github.com:acme/app.git",
      "https://github.com/acme",
      "https://github.com/acme/app/tree/main",
      "https://github.com/-acme/app",
      "https://github.com/acme/..",
      "https://github.com/acme/app name",
    ]) {
      assert.equal(parseGitHubRepository(input).ok, false, input);
    }
  });
});

describe("branchProblem", () => {
  it("accepts ordinary branch names", () => {
    for (const b of ["main", "feature/login", "release-1.2", "v2_x"]) {
      assert.equal(branchProblem(b), null, b);
    }
  });

  it("refuses names git check-ref-format refuses", () => {
    for (const b of [
      "",
      "-x",
      "a..b",
      "a b",
      "a~1",
      "a^",
      "a:b",
      "a*",
      "a[",
      "a\\b",
      "x.lock",
      "a/",
      "a//b",
      "a/.hidden",
      "@",
      "a@{1}",
      "end.",
    ]) {
      assert.notEqual(branchProblem(b), null, JSON.stringify(b));
    }
  });
});

describe("triggerGit", () => {
  it("names one repository in every field the controller compares", () => {
    const got = triggerGit("https://github.com/acme/app", " main ");
    assert.deepEqual(got, {
      ok: true,
      value: {
        repo_url: "https://github.com/acme/app",
        branch: "main",
        repo: "app",
        github_owner: "acme",
        github_repo: "app",
      },
    });
  });

  it("reports the first problem", () => {
    const got = triggerGit("https://github.com/acme/app", "a b");
    assert.equal(got.ok, false);
  });
});
