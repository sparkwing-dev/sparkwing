import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import { DeviceRunnerDetails } from "./DeviceRunnerDetails";

describe("runner enrollment consent", () => {
  for (const personal of [true, false]) {
    it(`shows the complete allowed label set for personal=${personal}`, () => {
      const labels = Array.from({ length: 32 }, (_, i) => i === 0 ? "a".repeat(128) : `deploy-pool-${i}`);
      const html = renderToStaticMarkup(<DeviceRunnerDetails team="acme" runner={{
        name: "worker", personal, repos: [], reserved_pools: [], trust: "", platform: "", launcher: false,
        allowed_labels: labels,
      }} />);
      assert.match(html, /Allowed labels/);
      for (const label of labels) assert.ok(html.includes(`>${label}</li>`), `hidden label ${label}`);
    });
  }
  it("states when no custom labels will be allowed", () => {
    const html = renderToStaticMarkup(<DeviceRunnerDetails team="acme" runner={{
      name: "worker", personal: true, repos: [], reserved_pools: [], trust: "", platform: "", launcher: false,
      allowed_labels: [],
    }} />);
    assert.match(html, /Allowed labels/);
    assert.match(html, /None/);
  });
});
