import { test as base } from "@playwright/test";
import { startStaticDashboard } from "./static-server";

export const test = base.extend({
  baseURL: async ({}, provide) => {
    const dashboard = await startStaticDashboard();
    try {
      await provide(dashboard.origin);
    } finally {
      await dashboard.close();
    }
  },
});

export { expect } from "@playwright/test";
