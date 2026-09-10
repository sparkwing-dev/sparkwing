# Runs UI exploration

Question: which layout makes it easier to find a run that needs attention and
understand its failure without losing your place?

Three throwaway variants of the existing `/runs` route are switchable through
`?variant=focus`, `?variant=workbench` and `?variant=board`. The original layout
remains at `/runs`. The floating switcher also responds to left/right arrow keys
outside form controls. The selected run and filters remain in the URL.

## Browser observations

Inspected the local dashboard on September 9, 2026, at desktop and phone widths.
Opened Home, Runs Activity and Overview, Crons, Queue, Capacity and Fleet.
Expanded a pipeline, opened a failed run and tried the status filter.

- Activity's 200-run window contained about 90 executions of one successful
  scheduled pipeline. Grouping repeat successes would leave more space for
  failures and less frequent work.
- The failed run's actual error appeared in its detail, while its list row said
  only that nodes failed. Put the failed node and its recorded error at the
  start of an investigation view.
- Selecting a run squeezes the list and node navigation into narrow columns.
  A two-pane layout preserves readable run names and puts node details inside
  the inspector.
- Runs occupied 770 CSS pixels on a 390-pixel viewport. Navigation and filters
  need a layout that fits the viewport, rather than hiding controls to the right.
- Home said services were healthy while Fleet reported no configured services.
  Report unconfigured or unknown coverage separately from healthy coverage.
- Home's deploy metrics included ordinary pipeline executions. Use "completed
  runs" unless a deployment is actually identified. Its latest-deploy helper
  selects the latest completed run without a deployment discriminator.

The running dashboard was v0.48.1. This branch also includes the cron cards and
trigger filtering already landed on main, so those shipped-source changes are
not proposals in this exploration.

## Variants

- **A, Focus inbox:** latest failed pipeline runs first, repeated successes
  condensed, with an expandable full activity list. Grouping is by repository
  and pipeline across branches, within the loaded and filtered window. This is
  not a complete incident or recovery model.
- **B, Investigation:** quick views, readable activity list and persistent
  inspector with Outcome, Steps and Context. The inspector uses the selected
  run's real node outcomes and errors. The first failed node is the first in
  the returned node list, not an inferred root cause.
- **C, Pipeline board:** latest outcomes organize pipelines into lanes. History
  bars open individual runs. Historical failures and cancelled runs remain
  visible without marking a recovered pipeline as currently failing.

Recommendation to evaluate: B as the everyday Runs workspace, with A available
as a quick entry point. C is useful for scanning many pipelines but overlaps
with the existing Overview. No variant has been selected for production.

## Run the exploration

From `web`, `npm run prototype` starts the development preview on port 3142.

The branch's Xwing installer sets `NEXT_PUBLIC_SPARKWING_UI_PROTOTYPE=1`, so
`xwing tool install --session <environment> --repo sparkwing` builds a candidate
binary containing the variants and the switcher. Ordinary builds leave the
prototype renderer disabled. This explicit build flag makes the switcher
available in the requested preview binary without enabling it in normal builds.

Xwing's Sparkwing candidate runtime does not support dashboard services.
After installing the candidate, `npm run prototype:preview` from `web` serves
its same generated `web/out` assets at `http://127.0.0.1:4344`. This is a separate
Node preview server, not the candidate binary's dashboard process. Its API proxy
reads the live dashboard on port 4343 and rejects mutations. Stop with Ctrl-C.

## Verification and scope

Checked the variants in Chromium with live data. Opened failure details and
steps, switched layouts by button and keyboard, filtered runs, and checked the
phone layout. Focus fits a 390-pixel viewport. Source lint and the candidate
build validate the throwaway implementation. No broad regression suite is
needed for an unmerged visual prototype.

Keep this branch out of main. A chosen design needs production implementation
and regression coverage before it lands.
