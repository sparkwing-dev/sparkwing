# Migrating to the next release

One breaking change, which affects Go code that sized a claim budget with the
recommendation helpers.

## The claim budget recommendation helpers are removed

**Before:** `controller.RecommendedClaimsPerMinute` (1200) and
`controller.RecommendedClaimsPerMinuteForSlots(slots)` sized a per-runner claim
budget as `1200 x max_concurrent`, on the model that every offer slot spends a
preparation plus an offer per round at the 500ms cadence.

**After:** both are gone. `controller.CompliantClaimPollsPerMinute()` reports
what an empty-queue poll cadence costs one runner in a minute, and
`controller.ClaimPollInterval` is the cadence it is worked from.

**What to do:** replace `RecommendedClaimsPerMinuteForSlots(n)` with a multiple
of `CompliantClaimPollsPerMinute()`. The `cloud` limits profile uses four times
it and `cloud-free` twice; an operator who wants the old headroom can pass
`--claims-per-runner-minute` with any number they like.

```go
// before
budget := controller.RecommendedClaimsPerMinuteForSlots(8) // 9600

// after
budget := controller.CompliantClaimPollsPerMinute() * 4 // 480
```

**Why:** no shipped runner spends a preparation and an offer per slot per
round. A pool runner claims one node at a time whatever its concurrency, so the
old figure was about twenty times what a compliant runner asks for and bounded
nothing. The budget also no longer charges an awarded claim, so a slot count
does not belong in it: what it bounds is empty polling.

**Edge cases:** a controller already running `--claims-per-runner-minute=9600`
keeps that setting; nothing reads the removed constants at runtime. A
controller that took its number from a limits profile gets the new, lower one,
which no compliant runner reaches.
