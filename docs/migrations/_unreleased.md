# Migrating to the next release

## Explicit resource measurements

Sustained CPU is nullable in profiles, observations and profile samples. An absent
value means unavailable; zero means measured zero. Go callers use `*float64` for
sustained CPU. Admission no longer substitutes peak CPU for missing sustained CPU.
Pod limits continue to use peak CPU.

Metric samples carry an explicit kind (`interval`, `command`, or `estimate`) and
separate CPU and memory availability. Producers must provide the kind and set each
availability flag from the corresponding measurement result. Historical records
without this metadata remain unknown. A command with zero CPU remains a command.

Automatic profile folding requires usable interval measurements for both CPU and memory. Command
exit totals and shared-process estimates remain diagnostic. Known gaps, including
mixed historical or estimated records, prevent that observation from entering a
profile. Process creation or exit between scans can therefore withhold learning.
Sampling does not establish unsampled peaks or complete execution coverage.

Interval memory is summed process RSS, which can count shared pages repeatedly.
Command exit RSS is a kernel high-water value and does not represent simultaneous
RSS summed across descendants.

The profile migration discards learned values from the previous accounting format,
including carried costs and contention floors. Explicit CPU and memory pins remain
unchanged. Upgrade binaries and recompile pipeline executables together: older
binaries cannot open the migrated store. Keep a backup before opening the store
with the new version; an older executable requires its compatible backup.
