# Migrating to the next release

Two breaking changes. The first affects a machine running `sparkwing cluster
gitcache` with the background fetch pass left at its default; the second
affects a caller that writes the credit settings route by hand.

## The gitcache refreshes on demand and stops polling by default

**Before:** the cache polled every mirror on a timer. `FETCH_INTERVAL`
(`--fetch-interval`) defaulted to a live interval, so a cache started with no
flags refreshed every mirror it held, whether or not anything read it, and a
mirror that failed to fetch was retried on every cycle. A run triggered seconds
after a push could still be told `not our ref`, because the push landed between
two passes.

**After:** a `git-upload-pack` clone refreshes the mirror it reads, and
`FETCH_INTERVAL` defaults to `0`, which turns the background pass off. The
on-demand refresh runs only outside `FETCH_FRESH_WINDOW`, whose default drops
from `15s` to `10s`, so one repository costs at most one origin fetch per
window no matter how many callers arrive. A cache started without
`--fetch-interval` therefore stops polling the moment it is upgraded.

**What to do:** nothing, if on-demand refresh is what you want; it is what
makes a run read the push that triggered it. To keep a keep-warm pass as well,
set the interval explicitly:

```
sparkwing cluster gitcache --fetch-interval 30s
```

The keep-warm pass is narrower than the old one: it refreshes only mirrors a
request touched in the last hour, and a mirror whose fetch fails backs off from
the interval and doubles to ten minutes rather than retrying every cycle.

**Why:** polling spent an origin fetch per mirror per cycle to serve refs that
nothing was reading, and still lost the race against a push that landed mid
cycle. Reading the refs is the event that says a mirror matters, so that is
where the fetch belongs.

**Edge cases:** `sparkwing.gitcache.fetch_duration` gains `reason`
(`on_demand`/`keep_warm`) and `failed` labels, so a dashboard that groups by
the metric's label set sees new series. A trigger loop that retried on `not our
ref` after ten seconds now waits fifteen, which puts the second attempt outside
the freshness window and on refreshed refs.

## The credit settings API drops billing_cpu_ceiling_cores

**Before:** the credit settings carried a billing ceiling. Every metered node
was billed at the class its cpu request fell in, held under
`billing_cpu_ceiling_cores`, so a cluster handing out smaller pods than its
plans asked for could bill what it actually gave. The field appeared on three
schemas in `api/openapi.yaml`:
`components.schemas.CreditSettings.properties.billing_cpu_ceiling_cores`,
`components.schemas.CreditState.properties.billing_cpu_ceiling_cores` and
`components.schemas.SetCreditSettings.properties.billing_cpu_ceiling_cores`.

**After:** all three are gone and `warm_cpu_class_cores` stands in their place.
It is the largest class the warm runner pool serves, not a billing cap: a node
above it starts a Kubernetes node sized to its own class, and zero starts one
for every class. A node is billed at the class it pinned, which is the class
its pod is given, so nothing needs to hold the billed class down any more.

**What to do:** stop sending the field. `PUT /api/v1/credits/settings` refuses
a body it does not recognise, so a request that still carries
`billing_cpu_ceiling_cores` answers `400` rather than ignoring it. A caller
reading `GET /api/v1/credits/settings` finds `warm_cpu_class_cores` where the
old field was; read it as the warm class, not as a ceiling. From the CLI:

```
sparkwing cluster credits settings --warm-cpu-class-cores 2
```

**Why:** the ceiling existed because a warm pod could be smaller than the plan
that asked for it, which made the billed class and the delivered class differ.
Routing a metered node to a Kubernetes node sized to its class closes that gap
at the source, so a knob that papered over it has nothing left to do.

**Edge cases:** the default is `2`, the class that stays warm and starts in
seconds; a larger class starts a node of its own in one to two minutes. Setting
it to `0` gives every class its own node. A cpu request above the largest class
in the rate table still fails the node with `unpriced_cpu_class`.
