#!/usr/bin/env bash
set -uo pipefail

if [[ $# -eq 0 ]]; then
  echo "usage: agent-trial-report.sh <trace.jsonl> [...]" >&2
  echo "traces land in \${TMPDIR:-/tmp}/sparkwing-agent-trial/<name>.jsonl" >&2
  exit 2
fi

python3 - "$@" <<'PY'
import collections
import json
import re
import sys
from datetime import datetime

# Ordered: the first pattern that matches wins, so the specific
# sparkwing verbs are tested before the generic shell readers that would
# otherwise swallow a `sparkwing ... | head` as file reading.
PHASES = [
    ("orient", r"sparkwing\s*$|sparkwing (info|commands|version)\b"
               r"|sparkwing pipeline list|sparkwing docs (list|guides)\b"),
    ("docs", r"sparkwing docs (read|search|all)\b"),
    ("examples", r"sparkwing examples\b"),
    ("choose-shape", r"sparkwing pipeline new [^|;&]*--help"),
    ("scaffold", r"sparkwing pipeline new\b"),
    ("verify", r"sparkwing (pipeline (lint|explain)|run|runs)\b"
               r"|go (build|test|vet)\b|gofmt"),
    ("read-repo", r"^\s*(cat|ls|find|grep|rg|head|tail|wc|tree|sed)\b"),
]


def phase_of(name, cmd):
    if name in ("Write", "Edit", "NotebookEdit", "file_change"):
        return "author"
    if name == "Read":
        return "read-repo"
    for phase, pat in PHASES:
        if re.search(pat, cmd):
            return phase
    return "other"


def subcommands(cmd):
    """Split a chained shell invocation into the actions it performs.

    Codex wraps its work in `/bin/zsh -lc "a && b && c"`. Left whole,
    three actions look like one and phase attribution loses them.
    """
    cmd = re.sub(r'^\s*\S*/?(?:ba|z)?sh\s+-\w*c\s+', "", cmd)
    parts = [p.strip() for p in re.split(r"&&|\|\||;|\n", cmd)]
    return [p for p in parts if p] or [cmd.strip()]


def parse(path):
    """Yield (timestamp|None, tool, command) per action, in order."""
    rows = []
    with open(path, errors="ignore") as fh:
        for line in fh:
            try:
                d = json.loads(line)
            except json.JSONDecodeError:
                continue
            t = None
            if d.get("timestamp"):
                try:
                    t = datetime.fromisoformat(
                        d["timestamp"].replace("Z", "+00:00")).timestamp()
                except ValueError:
                    pass
            calls = []
            if d.get("type") == "assistant":
                for b in d.get("message", {}).get("content") or []:
                    if b.get("type") == "tool_use":
                        inp = b.get("input") or {}
                        calls.append((b["name"],
                                      inp.get("command") or inp.get("file_path") or ""))
            elif d.get("type") == "item.started":
                item = d.get("item") or {}
                if item.get("type") == "command_execution":
                    calls = [("Bash", c) for c in subcommands(item.get("command", ""))]
                elif item.get("type") == "file_change":
                    calls = [("file_change", "")]
            rows.append({"t": t, "calls": calls})
    return rows


def report(path):
    rows = parse(path)
    stamps = [r["t"] for r in rows if r["t"]]
    total = (max(stamps) - min(stamps)) if stamps else None

    tool_s = 0.0
    phases = collections.defaultdict(int)
    turns = calls = 0
    for i, r in enumerate(rows):
        if not r["calls"]:
            continue
        turns += 1
        calls += len(r["calls"])
        if r["t"] is not None:
            nxt = next((rows[j]["t"] for j in range(i + 1, len(rows)) if rows[j]["t"]), None)
            if nxt:
                tool_s += nxt - r["t"]
        for name, cmd in r["calls"]:
            phases[phase_of(name, " ".join(cmd.split()))] += 1
    if not turns:
        return None
    return {"total": total, "tool": tool_s, "turns": turns,
            "calls": calls, "phases": phases}


pooled = collections.defaultdict(int)
tot_turns = tot_calls = timed_turns = 0
tot_total = tot_tool = 0.0
timed = untimed = 0

print(f"{'run':<22}{'total':>7}{'model':>8}{'tool':>7}{'turns':>7}{'calls':>7}{'s/turn':>8}")
for path in sys.argv[1:]:
    r = report(path)
    if not r:
        print(f"{path.rsplit('/', 1)[-1]:<22}  no actions in trace")
        continue
    name = path.rsplit("/", 1)[-1].removesuffix(".jsonl")
    if r["total"]:
        per = r["total"] / r["turns"]
        print(f"{name:<22}{r['total']:>6.0f}s{r['total'] - r['tool']:>7.0f}s"
              f"{r['tool']:>6.0f}s{r['turns']:>7}{r['calls']:>7}{per:>7.1f}s")
        tot_total += r["total"]
        tot_tool += r["tool"]
        timed_turns += r["turns"]
        timed += 1
    else:
        print(f"{name:<22}{'--':>7}{'--':>8}{'--':>7}"
              f"{r['turns']:>7}{r['calls']:>7}{'--':>8}")
        untimed += 1
    for p, n in r["phases"].items():
        pooled[p] += n
    tot_turns += r["turns"]
    tot_calls += r["calls"]

if not tot_calls:
    sys.exit(0)

print()
if timed:
    print(f"{tot_total:.0f}s across {timed} timed run(s): "
          f"{tot_total - tot_tool:.0f}s model, {tot_tool:.0f}s tool")
    print(f"{timed_turns} turns at {tot_total / timed_turns:.1f}s each")
    if untimed:
        print(f"({untimed} untimed run(s) contribute turns and calls but no seconds)")
print()
print("calls by phase -- each is work the CLI made the agent do:")
for p, n in sorted(pooled.items(), key=lambda kv: -kv[1]):
    share = n / tot_calls * 100
    bar = "#" * round(share / 4)
    print(f"  {p:<14}{n:>4}{share:>5.0f}%  {bar}")
PY
