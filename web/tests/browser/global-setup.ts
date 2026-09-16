import { prepareAuthenticatedDashboardFixture } from "./authenticated-server";

export default async function globalSetup(): Promise<() => Promise<void>> {
  return prepareAuthenticatedDashboardFixture();
}
