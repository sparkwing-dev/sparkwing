# Migrating to the next release

One breaking change. It affects a machine running `sparkwing cluster gitcache`
with the background fetch pass left at its default; nothing else needs action.

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
