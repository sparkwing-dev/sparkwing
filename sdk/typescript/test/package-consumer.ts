import type { Job, Plan } from "@sparkwing/sdk";

export function declareChildren(plan: Plan): Job<unknown> {
  return plan.job("release").startsChildren("deploy", "smoke").startsChildrenIn("gh:123", "publish").requires("os=linux");
}
