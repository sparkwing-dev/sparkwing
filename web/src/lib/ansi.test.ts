import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { ansiToHtml, stripAnsi } from "./ansi";

describe("ansiToHtml", () => {
  it("keeps allow-listed colors", () => {
    assert.equal(
      ansiToHtml("\x1b[31mred\x1b[0m"),
      '<span class="text-red-400">red</span>',
    );
  });

  it("normalizes zero-padded parameters", () => {
    assert.equal(
      ansiToHtml("\x1b[01;34mdir\x1b[00m"),
      '<span class="font-bold"><span class="text-blue-400">dir</span></span>',
    );
    assert.equal(
      ansiToHtml("\x1b[031mX\x1b[00m"),
      '<span class="text-red-400">X</span>',
    );
  });

  it("treats empty parameters as a reset", () => {
    assert.equal(
      ansiToHtml("\x1b[31ma\x1b[mb"),
      '<span class="text-red-400">a</span>b',
    );
  });

  it("drops a compound sequence whole rather than inventing attributes", () => {
    assert.equal(ansiToHtml("\x1b[38;5;214mfake\x1b[0m"), "fake");
    assert.equal(ansiToHtml("\x1b[38;5;31mX\x1b[0m"), "X");
    assert.equal(ansiToHtml("\x1b[38;2;1;2;4mX\x1b[0m"), "X");
    assert.equal(ansiToHtml("\x1b[48;2;1;2;4mX"), "X");
    assert.equal(ansiToHtml("\x1b[1;38;5;214mfake"), "fake");
  });

  it("drops a truncated extended color", () => {
    assert.equal(ansiToHtml("\x1b[38;5mX"), "X");
    assert.equal(ansiToHtml("\x1b[38mX"), "X");
  });

  it("escapes markup in log text", () => {
    assert.equal(ansiToHtml("<b>&</b>"), "&lt;b&gt;&amp;&lt;/b&gt;");
  });
});

describe("stripAnsi", () => {
  it("removes sgr sequences", () => {
    assert.equal(stripAnsi("\x1b[01;34mdir\x1b[00m"), "dir");
  });
});
