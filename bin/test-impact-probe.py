#!/usr/bin/env python3
"""Estimate an optimistic Go package test selection against recent changes."""

import argparse
import json
import os
import subprocess
from collections import Counter
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MODULES = (Path("."), Path(".sparkwing"))


def run(*args, cwd=ROOT):
    return subprocess.check_output(args, cwd=cwd, env={**os.environ, "GOWORK": "off"}, text=True)


def packages():
    result = {}
    for module in MODULES:
        output = run("go", "list", "-json", "./...", cwd=ROOT / module)
        decoder = json.JSONDecoder()
        while output.strip():
            item, offset = decoder.raw_decode(output.lstrip())
            output = output.lstrip()[offset:]
            directory = Path(item["Dir"]).relative_to(ROOT)
            result[item["ImportPath"]] = {
                "directory": directory,
                "imports": set(item.get("Imports", []) + item.get("TestImports", []) + item.get("XTestImports", [])),
            }
    return result


def select(files, graph):
    files = list(files)
    build_inputs = {"go.mod", "go.sum", ".sparkwing/go.mod", ".sparkwing/go.sum", "go.work"}
    if any(f in build_inputs or f.startswith(".github/workflows/") for f in files):
        return set(graph), "global build input"
    go_files = [f for f in files if f.endswith(".go")]
    if any(not any(Path(f).parent == p["directory"] for p in graph.values()) for f in go_files):
        return set(graph), "unmapped Go file"
    seeds = {name for name, pkg in graph.items() if any(Path(f).parent == pkg["directory"] for f in go_files)}
    # Test-only changes cannot change an imported package's production behavior.
    production = {
        name for name, pkg in graph.items()
        if any(Path(f).parent == pkg["directory"] and not f.endswith("_test.go") for f in go_files)
    }
    selected = set(seeds)
    frontier = set(production)
    while frontier:
        frontier = {name for name, pkg in graph.items() if name not in selected and pkg["imports"] & frontier}
        selected.update(frontier)
    return selected, "Go package graph"


def changed(revision):
    return run("git", "diff-tree", "--no-commit-id", "--name-only", "-r", revision).splitlines()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--history", type=int, default=60)
    args = parser.parse_args()
    graph = packages()
    revisions = run("git", "rev-list", "--no-merges", f"--max-count={args.history}", "HEAD").splitlines()
    rows = []
    for rev in revisions:
        files = changed(rev)
        selected, reason = select(files, graph)
        if files:
            rows.append({"revision": rev[:10], "files": files, "selected": sorted(selected), "reason": reason})
    print(json.dumps({"package_count": len(graph), "sample_count": len(rows), "selection_counts": dict(sorted(Counter(len(r["selected"]) for r in rows).items())), "rows": rows}, indent=2))


if __name__ == "__main__":
    main()
