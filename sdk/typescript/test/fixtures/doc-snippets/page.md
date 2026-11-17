# Fixture

A fragment that type-checks:

```ts
const build = plan.job("build", async (ctx) => {
  await ctx.exec(["npm", "run", "build"]).run();
});
plan.job("deploy", () => undefined).needs(build);
```

The method onboarding guessed, which the SDK does not have:

```ts
plan.job("branch", async (ctx) => {
  const out = await ctx.exec(["git", "branch", "--show-current"]).output();
  return out;
});
```

A complete module:

```ts
import { definePipeline, npm } from "@sparkwing/sdk";

definePipeline({
  name: "ci",
  plan(plan) {
    plan.job("test", async (ctx) => {
      await ctx.exec(["npm", "test"]).run();
    }).cache(npm());
  },
});
```

<!-- doccheck:skip illustrates a wrong call -->
```ts
plan.nonexistent();
```
