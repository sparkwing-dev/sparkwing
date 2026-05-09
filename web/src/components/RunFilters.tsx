"use client";

// Shared run-filter machinery used by /runs (Activity view) and the
// PipelineOverview component (By pipeline view). Filter state lives
// in URL params so it persists across the pivot toggle, survives
// reloads, and can be shared as a link.
//
// Encoded URL params:
//   status, repo, pipeline, branch, commit, tag        → comma-joined includes
//   nstatus, nrepo, npipeline, nbranch, ncommit, ntag  → comma-joined excludes
//   startedAfter, startedBefore, finishedAfter, finishedBefore → loose date strings
//   q → free-text search (space = AND, "-" prefix = exclude)

import { useCallback, useEffect, useRef, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import type { Run, PipelineMeta } from "@/lib/api";

// ─── URL state hook ────────────────────────────────────────────────────────

const FILTER_URL_KEYS = [
  "status",
  "nstatus",
  "repo",
  "nrepo",
  "pipeline",
  "npipeline",
  "branch",
  "nbranch",
  "commit",
  "ncommit",
  "tag",
  "ntag",
  "startedAfter",
  "startedBefore",
  "finishedAfter",
  "finishedBefore",
  "q",
];
const FILTER_STORAGE_KEY = "sparkwing.runFilters";

export function useUrlFilterState() {
  const searchParams = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();

  const setParams = useCallback(
    (updates: Record<string, string | string[]>) => {
      const next = new URLSearchParams(searchParams.toString());
      for (const [key, val] of Object.entries(updates)) {
        const empty = val === "" || (Array.isArray(val) && val.length === 0);
        if (empty) next.delete(key);
        else next.set(key, Array.isArray(val) ? val.join(",") : val);
      }
      const qs = next.toString();
      router.replace(qs ? `${pathname}?${qs}` : pathname, { scroll: false });
    },
    [searchParams, router, pathname],
  );

  const restored = useRef(false);
  useEffect(() => {
    if (restored.current) return;
    restored.current = true;
    const hasAny = FILTER_URL_KEYS.some((k) => searchParams.get(k));
    if (hasAny) return;
    const saved = sessionStorage.getItem(FILTER_STORAGE_KEY);
    if (!saved) return;
    const savedParams = new URLSearchParams(saved);
    const next = new URLSearchParams(searchParams.toString());
    let added = false;
    for (const k of FILTER_URL_KEYS) {
      const v = savedParams.get(k);
      if (v) {
        next.set(k, v);
        added = true;
      }
    }
    if (!added) return;
    router.replace(`${pathname}?${next.toString()}`, { scroll: false });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (typeof window === "undefined") return;
    const filterOnly = new URLSearchParams();
    for (const k of FILTER_URL_KEYS) {
      const v = searchParams.get(k);
      if (v) filterOnly.set(k, v);
    }
    const qs = filterOnly.toString();
    if (qs) sessionStorage.setItem(FILTER_STORAGE_KEY, qs);
    else sessionStorage.removeItem(FILTER_STORAGE_KEY);
  }, [searchParams]);

  const getList = (key: string): string[] => {
    const v = searchParams.get(key);
    return v ? v.split(",").filter(Boolean) : [];
  };
  const getStr = (key: string): string => searchParams.get(key) || "";

  return {
    filterStatus: getList("status"),
    setFilterStatus: (v: string[]) => setParams({ status: v }),
    excludeStatus: getList("nstatus"),
    setExcludeStatus: (v: string[]) => setParams({ nstatus: v }),

    filterRepo: getList("repo"),
    setFilterRepo: (v: string[]) => setParams({ repo: v }),
    excludeRepo: getList("nrepo"),
    setExcludeRepo: (v: string[]) => setParams({ nrepo: v }),

    filterPipeline: getList("pipeline"),
    setFilterPipeline: (v: string[]) => setParams({ pipeline: v }),
    excludePipeline: getList("npipeline"),
    setExcludePipeline: (v: string[]) => setParams({ npipeline: v }),

    filterBranch: getList("branch"),
    setFilterBranch: (v: string[]) => setParams({ branch: v }),
    excludeBranch: getList("nbranch"),
    setExcludeBranch: (v: string[]) => setParams({ nbranch: v }),

    filterCommit: getList("commit"),
    setFilterCommit: (v: string[]) => setParams({ commit: v }),
    excludeCommit: getList("ncommit"),
    setExcludeCommit: (v: string[]) => setParams({ ncommit: v }),

    filterTag: getList("tag"),
    setFilterTag: (v: string[]) => setParams({ tag: v }),
    excludeTag: getList("ntag"),
    setExcludeTag: (v: string[]) => setParams({ ntag: v }),

    startedAfter: getStr("startedAfter"),
    setStartedAfter: (v: string) => setParams({ startedAfter: v }),
    startedBefore: getStr("startedBefore"),
    setStartedBefore: (v: string) => setParams({ startedBefore: v }),
    finishedAfter: getStr("finishedAfter"),
    setFinishedAfter: (v: string) => setParams({ finishedAfter: v }),
    finishedBefore: getStr("finishedBefore"),
    setFinishedBefore: (v: string) => setParams({ finishedBefore: v }),

    filterText: getStr("q"),
    setFilterText: (v: string) => setParams({ q: v }),
  };
}

export type RunFilterState = ReturnType<typeof useUrlFilterState>;

// ─── Helpers ───────────────────────────────────────────────────────────────

export function repoLabel(r: Run): string {
  const raw = r.repo || r.github_repo || "unknown";
  const slash = raw.lastIndexOf("/");
  return slash >= 0 ? raw.slice(slash + 1) : raw;
}

export function parseLooseDate(s: string): number | null {
  const t = s.trim();
  if (!t) return null;
  if (/^\d{4}-\d{2}-\d{2}$/.test(t)) {
    const d = new Date(t + "T00:00");
    return isNaN(d.getTime()) ? null : d.getTime();
  }
  if (/^\d{1,2}:\d{2}(:\d{2})?$/.test(t)) {
    const today = new Date();
    const ymd = `${today.getFullYear()}-${String(today.getMonth() + 1).padStart(2, "0")}-${String(today.getDate()).padStart(2, "0")}`;
    const hhmm = t.length === 4 || t.length === 5 ? t.padStart(5, "0") : t;
    const d = new Date(`${ymd}T${hhmm}`);
    return isNaN(d.getTime()) ? null : d.getTime();
  }
  if (/^\d{4}-\d{2}-\d{2}[ T]\d{1,2}:\d{2}(:\d{2})?$/.test(t)) {
    const d = new Date(t.replace(" ", "T"));
    return isNaN(d.getTime()) ? null : d.getTime();
  }
  const d = new Date(t);
  return isNaN(d.getTime()) ? null : d.getTime();
}

export function fmtDateChip(local: string): string {
  if (!local) return "";
  const d = new Date(local);
  if (isNaN(d.getTime())) return local;
  return d.toLocaleString([], {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

interface SearchTerm {
  text: string;
  mode: "include" | "exclude";
}

export function parseSearch(s: string): SearchTerm[] {
  const tokens = s.trim().split(/\s+/).filter(Boolean);
  const out: SearchTerm[] = [];
  let pendingNot = false;
  for (const t of tokens) {
    if (t === "-") {
      pendingNot = true;
      continue;
    }
    const attached = t.startsWith("-") && t.length > 1;
    const text = attached ? t.slice(1) : t;
    out.push({
      text,
      mode: pendingNot || attached ? "exclude" : "include",
    });
    pendingNot = false;
  }
  return out;
}

export function serializeSearch(terms: SearchTerm[]): string {
  return terms
    .map((t) => (t.mode === "exclude" ? `-${t.text}` : t.text))
    .join(" ");
}

// runMatchesFilter returns true if a run passes every active filter
// in `s`. Used by both views to filter their data identically.
export function runMatchesFilter(
  r: Run,
  s: RunFilterState,
  pipelineMeta: Record<string, PipelineMeta>,
): boolean {
  const repo = repoLabel(r);
  const branch = r.git_branch || "";
  const sha7 = r.git_sha ? r.git_sha.slice(0, 7) : "";
  const tags = pipelineMeta[r.pipeline]?.tags || [];

  if (s.excludeStatus.includes(r.status)) return false;
  if (s.excludeRepo.includes(repo)) return false;
  if (s.excludePipeline.includes(r.pipeline)) return false;
  if (s.excludeBranch.includes(branch)) return false;
  if (sha7 && s.excludeCommit.includes(sha7)) return false;
  if (s.excludeTag.some((t) => tags.includes(t))) return false;

  if (s.filterStatus.length && !s.filterStatus.includes(r.status)) return false;
  if (s.filterRepo.length && !s.filterRepo.includes(repo)) return false;
  if (s.filterPipeline.length && !s.filterPipeline.includes(r.pipeline))
    return false;
  if (s.filterBranch.length && !s.filterBranch.includes(branch)) return false;
  if (s.filterCommit.length && !s.filterCommit.includes(sha7)) return false;
  if (s.filterTag.length && !s.filterTag.some((t) => tags.includes(t)))
    return false;

  const startedTs = new Date(r.started_at).getTime();
  const sa = parseLooseDate(s.startedAfter);
  const sb = parseLooseDate(s.startedBefore);
  if (sa !== null && startedTs < sa) return false;
  if (sb !== null && startedTs > sb) return false;

  const fa = parseLooseDate(s.finishedAfter);
  const fb = parseLooseDate(s.finishedBefore);
  if (fa !== null || fb !== null) {
    if (!r.finished_at) return false;
    const finishedTs = new Date(r.finished_at).getTime();
    if (fa !== null && finishedTs < fa) return false;
    if (fb !== null && finishedTs > fb) return false;
  }

  if (s.filterText.trim()) {
    const hay = [
      r.id,
      r.pipeline,
      repo,
      r.git_branch,
      r.git_sha,
      r.error,
      r.trigger_source,
      r.status,
    ]
      .filter(Boolean)
      .join(" ")
      .toLowerCase();
    const terms = s.filterText.trim().split(/\s+/).filter(Boolean);
    const incl: string[] = [];
    const excl: string[] = [];
    let pendingNot = false;
    for (const t of terms) {
      if (t === "-") {
        pendingNot = true;
        continue;
      }
      const attached = t.startsWith("-") && t.length > 1;
      const term = (attached ? t.slice(1) : t).toLowerCase();
      if (pendingNot || attached) excl.push(term);
      else incl.push(term);
      pendingNot = false;
    }
    if (incl.some((t) => !hay.includes(t))) return false;
    if (excl.some((t) => hay.includes(t))) return false;
  }

  return true;
}

// activeFilterCount returns how many filter slots are currently set.
// Used to show the "clear all" button and gate the chip strip.
export function activeFilterCount(s: RunFilterState): number {
  return (
    s.filterStatus.length +
    s.filterRepo.length +
    s.filterPipeline.length +
    s.filterBranch.length +
    s.filterCommit.length +
    s.filterTag.length +
    s.excludeStatus.length +
    s.excludeRepo.length +
    s.excludePipeline.length +
    s.excludeBranch.length +
    s.excludeCommit.length +
    s.excludeTag.length +
    (s.startedAfter || s.startedBefore ? 1 : 0) +
    (s.finishedAfter || s.finishedBefore ? 1 : 0) +
    (s.filterText.trim() ? 1 : 0)
  );
}

// clearAllFilters wipes every facet at once.
export function clearAllFilters(s: RunFilterState) {
  s.setFilterRepo([]);
  s.setFilterPipeline([]);
  s.setFilterBranch([]);
  s.setFilterStatus([]);
  s.setFilterTag([]);
  s.setFilterCommit([]);
  s.setStartedAfter("");
  s.setStartedBefore("");
  s.setFinishedAfter("");
  s.setFinishedBefore("");
  s.setFilterText("");
  s.setExcludeStatus([]);
  s.setExcludeRepo([]);
  s.setExcludePipeline([]);
  s.setExcludeBranch([]);
  s.setExcludeCommit([]);
  s.setExcludeTag([]);
}

// ─── Filter bar ────────────────────────────────────────────────────────────

export interface FilterGroup {
  key: string;
  label: string;
  values: string[];
  set: (v: string[]) => void;
  options: string[];
  color?: string;
  activeBg: string;
  activeText: string;
  excludeValues?: string[];
  setExclude?: (v: string[]) => void;
}

export interface DateGroup {
  startedAfter: string;
  startedBefore: string;
  finishedAfter: string;
  finishedBefore: string;
  setStartedAfter: (v: string) => void;
  setStartedBefore: (v: string) => void;
  setFinishedAfter: (v: string) => void;
  setFinishedBefore: (v: string) => void;
}

// buildGroupsFromState constructs the standard 6-facet group config
// from a RunFilterState plus the option lists derived from the
// caller's runs data.
export function buildGroupsFromState(
  s: RunFilterState,
  options: {
    statuses: string[];
    repos: string[];
    pipelines: string[];
    branches: string[];
    commits: string[];
    tags: string[];
  },
): FilterGroup[] {
  return [
    {
      key: "status",
      label: "STATUS",
      values: s.filterStatus,
      set: s.setFilterStatus,
      excludeValues: s.excludeStatus,
      setExclude: s.setExcludeStatus,
      options: options.statuses,
      color: "text-emerald-400",
      activeBg: "bg-emerald-500/15",
      activeText: "text-emerald-300",
    },
    {
      key: "repo",
      label: "REPO",
      values: s.filterRepo,
      set: s.setFilterRepo,
      excludeValues: s.excludeRepo,
      setExclude: s.setExcludeRepo,
      options: options.repos,
      color: "text-cyan-400",
      activeBg: "bg-cyan-500/15",
      activeText: "text-cyan-300",
    },
    {
      key: "pipeline",
      label: "PIPELINE",
      values: s.filterPipeline,
      set: s.setFilterPipeline,
      excludeValues: s.excludePipeline,
      setExclude: s.setExcludePipeline,
      options: options.pipelines,
      color: "text-violet-400",
      activeBg: "bg-violet-500/15",
      activeText: "text-violet-300",
    },
    {
      key: "branch",
      label: "BRANCH",
      values: s.filterBranch,
      set: s.setFilterBranch,
      excludeValues: s.excludeBranch,
      setExclude: s.setExcludeBranch,
      options: options.branches,
      color: "text-amber-400",
      activeBg: "bg-amber-500/15",
      activeText: "text-amber-300",
    },
    {
      key: "commit",
      label: "COMMIT",
      values: s.filterCommit,
      set: s.setFilterCommit,
      excludeValues: s.excludeCommit,
      setExclude: s.setExcludeCommit,
      options: options.commits,
      color: "text-sky-400",
      activeBg: "bg-sky-500/15",
      activeText: "text-sky-300",
    },
    {
      key: "tag",
      label: "TAG",
      values: s.filterTag,
      set: s.setFilterTag,
      excludeValues: s.excludeTag,
      setExclude: s.setExcludeTag,
      options: options.tags,
      color: "text-pink-400",
      activeBg: "bg-pink-500/15",
      activeText: "text-pink-300",
    },
  ];
}

// computeOptions derives the available filter options from a runs
// list and pipeline metadata.
export function computeOptions(
  runs: Run[],
  pipelineMeta: Record<string, PipelineMeta>,
) {
  return {
    statuses: ["success", "failed", "running", "cancelled"],
    repos: [...new Set(runs.map(repoLabel))].sort(),
    pipelines: [...new Set(runs.map((r) => r.pipeline))].sort(),
    branches: [
      ...new Set(runs.map((r) => r.git_branch || "").filter(Boolean)),
    ].sort(),
    commits: [
      ...new Set(
        runs
          .map((r) => (r.git_sha ? r.git_sha.slice(0, 7) : ""))
          .filter(Boolean),
      ),
    ].sort(),
    tags: [
      ...new Set(Object.values(pipelineMeta).flatMap((m) => m.tags || [])),
    ].sort(),
  };
}

const toggleFilterHelper = (
  arr: string[],
  set: (v: string[]) => void,
  val: string,
) => {
  set(arr.includes(val) ? arr.filter((v) => v !== val) : [...arr, val]);
};

export function FullFilterBar({
  openDropdown,
  setOpenDropdown,
  groups,
  dateGroup,
  searchText,
  setSearchText,
  onClearAll,
}: {
  openDropdown: string | null;
  setOpenDropdown: (v: string | null) => void;
  groups: FilterGroup[];
  dateGroup?: DateGroup;
  searchText?: string;
  setSearchText?: (s: string) => void;
  onClearAll: () => void;
}) {
  const [search, setSearch] = useState<Record<string, string>>({});
  const searchTerms = searchText ? parseSearch(searchText) : [];
  const removeSearchTerm = (idx: number) => {
    if (!setSearchText) return;
    setSearchText(serializeSearch(searchTerms.filter((_, i) => i !== idx)));
  };
  const toggleSearchTerm = (idx: number) => {
    if (!setSearchText) return;
    setSearchText(
      serializeSearch(
        searchTerms.map((t, i) =>
          i === idx
            ? { ...t, mode: t.mode === "include" ? "exclude" : "include" }
            : t,
        ),
      ),
    );
  };

  const activeCount =
    groups.reduce(
      (n, g) => n + g.values.length + (g.excludeValues || []).length,
      0,
    ) +
    (dateGroup &&
    (dateGroup.startedAfter ||
      dateGroup.startedBefore ||
      dateGroup.finishedAfter ||
      dateGroup.finishedBefore)
      ? 1
      : 0) +
    ((searchText || "").trim() ? 1 : 0);

  return (
    <div className="px-2 py-1.5 flex items-start gap-2 w-full">
      {activeCount > 0 && (
        <button
          onClick={onClearAll}
          title="clear all filters"
          className="text-[var(--muted)] hover:text-red-400 text-base leading-none shrink-0 pt-1.5 px-1"
        >
          ×
        </button>
      )}
      <div className="flex flex-col gap-1.5 shrink-0 min-w-[32rem]">
        <div className="flex items-center gap-1 flex-wrap">
          {groups.map((f) => {
            const q = (search[f.key] || "").toLowerCase();
            const filteredOpts = q
              ? f.options.filter((opt) => opt.toLowerCase().includes(q))
              : f.options;
            const incCount = f.values.length;
            const excCount = (f.excludeValues || []).length;
            const anyActive = incCount + excCount > 0;
            return (
              <div key={f.key} className="relative">
                <button
                  onClick={() =>
                    setOpenDropdown(openDropdown === f.key ? null : f.key)
                  }
                  className={`px-2 py-0.5 rounded text-[10px] font-bold tracking-wider transition-colors ${
                    anyActive
                      ? `${f.activeBg} ${f.activeText}`
                      : `text-[var(--muted)] hover:${f.color || ""}`
                  }`}
                >
                  {f.label}
                  {anyActive && (
                    <>
                      {" ("}
                      {incCount > 0 && <span>{incCount}</span>}
                      {incCount > 0 && excCount > 0 && (
                        <span className="text-[var(--muted)]">, </span>
                      )}
                      {excCount > 0 && (
                        <span className="text-red-300">−{excCount}</span>
                      )}
                      {")"}
                    </>
                  )}{" "}
                  <span className="text-[8px]">▾</span>
                </button>
                {openDropdown === f.key && (
                  <div className="absolute top-full left-0 mt-1 bg-[var(--surface)] border border-[var(--border)] rounded-lg shadow-lg z-50 min-w-[200px] max-h-72 flex flex-col">
                    <div className="p-2 border-b border-[var(--border)] shrink-0">
                      <input
                        type="search"
                        autoFocus
                        placeholder={`search ${f.label.toLowerCase()}...`}
                        value={search[f.key] || ""}
                        onChange={(e) =>
                          setSearch((prev) => ({
                            ...prev,
                            [f.key]: e.target.value,
                          }))
                        }
                        className="w-full bg-[var(--background)] border border-[var(--border)] rounded px-2 py-1 text-xs"
                      />
                    </div>
                    <div className="overflow-y-auto">
                      {f.values.length > 0 && (
                        <button
                          onClick={() => f.set([])}
                          className="w-full text-left px-3 py-1.5 text-xs hover:bg-[var(--surface-raised)] text-[var(--muted)] border-b border-[var(--border)]"
                        >
                          Clear all
                        </button>
                      )}
                      {filteredOpts.map((opt) => {
                        const isSelected = f.values.includes(opt);
                        const isExcluded = (f.excludeValues || []).includes(
                          opt,
                        );
                        return (
                          <div
                            key={opt}
                            className={`flex items-center hover:bg-[var(--surface-raised)] ${isExcluded ? "opacity-70" : ""}`}
                          >
                            <button
                              onClick={() =>
                                toggleFilterHelper(f.values, f.set, opt)
                              }
                              className={`flex-1 text-left px-3 py-1.5 text-xs font-mono flex items-center gap-2 ${
                                isSelected ? f.activeText : ""
                              } ${isExcluded ? "text-red-300 line-through" : ""}`}
                            >
                              <span
                                className={`w-3.5 h-3.5 rounded border flex items-center justify-center text-[10px] ${
                                  isSelected
                                    ? `${f.activeBg} border-current`
                                    : isExcluded
                                      ? "bg-red-500/15 border-red-400 text-red-400"
                                      : "border-[var(--border)]"
                                }`}
                              >
                                {isSelected ? "✓" : isExcluded ? "−" : ""}
                              </span>
                              {f.key === "branch" ? `⎇ ${opt}` : opt}
                            </button>
                            {f.setExclude && (
                              <button
                                onClick={(e) => {
                                  e.stopPropagation();
                                  if (!f.setExclude) return;
                                  const exc = f.excludeValues || [];
                                  if (exc.includes(opt)) {
                                    f.setExclude(exc.filter((v) => v !== opt));
                                  } else {
                                    f.setExclude([...exc, opt]);
                                    if (f.values.includes(opt))
                                      f.set(f.values.filter((v) => v !== opt));
                                  }
                                }}
                                title={
                                  isExcluded ? "remove exclusion" : "exclude"
                                }
                                className={`px-2 py-1.5 text-[11px] hover:bg-red-500/10 ${
                                  isExcluded
                                    ? "text-red-300"
                                    : "text-[var(--muted)] hover:text-red-300"
                                }`}
                              >
                                −
                              </button>
                            )}
                          </div>
                        );
                      })}
                      {filteredOpts.length === 0 && (
                        <div className="px-3 py-2 text-[var(--muted)] text-xs">
                          {q ? "no matches" : "no options yet"}
                        </div>
                      )}
                    </div>
                  </div>
                )}
              </div>
            );
          })}
          {dateGroup && (
            <DateFilterButton
              group={dateGroup}
              open={openDropdown === "date"}
              onToggle={() =>
                setOpenDropdown(openDropdown === "date" ? null : "date")
              }
            />
          )}
        </div>
        {setSearchText && (
          <input
            type="search"
            value={searchText || ""}
            onChange={(e) => setSearchText(e.target.value)}
            placeholder="Search: space between filters. Use prefix - to negate."
            className="bg-[var(--background)] border border-[var(--border)] rounded px-2 py-1 text-xs w-full"
          />
        )}
      </div>
      <div className="flex-1 min-w-0 flex flex-wrap gap-1 items-start content-start">
        {groups.flatMap((f) =>
          f.values.map((v) => (
            <span
              key={`${f.key}-inc-${v}`}
              className={`inline-flex items-center gap-1 ${f.activeBg} ${f.activeText} px-2 py-0.5 rounded text-xs font-mono`}
            >
              {f.key === "branch" ? `⎇ ${v}` : v}
              <button
                onClick={() => toggleFilterHelper(f.values, f.set, v)}
                className="hover:text-white"
              >
                ×
              </button>
            </span>
          )),
        )}
        {groups.flatMap((f) =>
          (f.excludeValues || []).map((v) => (
            <span
              key={`${f.key}-exc-${v}`}
              className={`inline-flex items-center gap-1 ${f.activeBg} ${f.activeText} px-2 py-0.5 rounded text-xs font-mono line-through`}
            >
              {f.key === "branch" ? `⎇ ${v}` : v}
              <button
                onClick={() => {
                  if (!f.setExclude) return;
                  f.setExclude((f.excludeValues || []).filter((x) => x !== v));
                }}
                className="text-red-400 hover:text-red-300 no-underline font-bold"
              >
                ×
              </button>
            </span>
          )),
        )}
        {dateGroup && (dateGroup.startedAfter || dateGroup.startedBefore) && (
          <span className="inline-flex items-center gap-1 bg-orange-500/15 text-orange-300 px-2 py-0.5 rounded text-xs font-mono">
            started{" "}
            {dateGroup.startedAfter &&
              `after ${fmtDateChip(dateGroup.startedAfter)}`}
            {dateGroup.startedAfter && dateGroup.startedBefore && " · "}
            {dateGroup.startedBefore &&
              `before ${fmtDateChip(dateGroup.startedBefore)}`}
            <button
              onClick={() => {
                dateGroup.setStartedAfter("");
                dateGroup.setStartedBefore("");
              }}
              className="hover:text-white"
            >
              ×
            </button>
          </span>
        )}
        {dateGroup && (dateGroup.finishedAfter || dateGroup.finishedBefore) && (
          <span className="inline-flex items-center gap-1 bg-orange-500/15 text-orange-300 px-2 py-0.5 rounded text-xs font-mono">
            finished{" "}
            {dateGroup.finishedAfter &&
              `after ${fmtDateChip(dateGroup.finishedAfter)}`}
            {dateGroup.finishedAfter && dateGroup.finishedBefore && " · "}
            {dateGroup.finishedBefore &&
              `before ${fmtDateChip(dateGroup.finishedBefore)}`}
            <button
              onClick={() => {
                dateGroup.setFinishedAfter("");
                dateGroup.setFinishedBefore("");
              }}
              className="hover:text-white"
            >
              ×
            </button>
          </span>
        )}
        {searchTerms.map((t, i) => {
          const inc = t.mode === "include";
          return (
            <span
              key={`search-${i}-${t.text}`}
              className={`inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-mono ${
                inc
                  ? "bg-slate-500/15 text-slate-200"
                  : "bg-red-500/15 text-red-300 line-through"
              }`}
            >
              <button
                onClick={() => toggleSearchTerm(i)}
                title={`flip to ${inc ? "exclude" : "include"}`}
                className="hover:text-white no-underline opacity-70 hover:opacity-100"
              >
                {inc ? "+" : "−"}
              </button>
              {inc ? t.text : `NOT ${t.text}`}
              <button
                onClick={() => removeSearchTerm(i)}
                className="hover:text-white no-underline"
              >
                ×
              </button>
            </span>
          );
        })}
      </div>
    </div>
  );
}

function DateFilterButton({
  group,
  open,
  onToggle,
}: {
  group: DateGroup;
  open: boolean;
  onToggle: () => void;
}) {
  const activeCount =
    (group.startedAfter || group.startedBefore ? 1 : 0) +
    (group.finishedAfter || group.finishedBefore ? 1 : 0);
  const active = activeCount > 0;
  const inputCls =
    "mt-1 w-full bg-[var(--background)] border border-[var(--border)] rounded px-2 py-1 text-xs font-mono text-[var(--foreground)]";
  return (
    <div className="relative">
      <button
        onClick={onToggle}
        className={`px-2 py-0.5 rounded text-[10px] font-bold tracking-wider transition-colors ${
          active
            ? "bg-orange-500/15 text-orange-300"
            : "text-[var(--muted)] hover:text-orange-400"
        }`}
      >
        DATE{active ? ` (${activeCount})` : ""}{" "}
        <span className="text-[8px]">▾</span>
      </button>
      {open && (
        <div className="absolute top-full left-0 mt-1 bg-[var(--surface)] border border-[var(--border)] rounded-lg shadow-lg z-50 min-w-[280px] p-3 space-y-3">
          <div className="text-[9px] text-[var(--muted)]">
            accepts partial dates, times, or both — e.g. <code>2026-05-09</code>
            , <code>14:30</code>, or <code>2026-05-09 14:30</code>
          </div>
          <div className="space-y-2">
            <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
              Started
            </div>
            <label className="block text-[10px] text-[var(--muted)]">
              after
              <input
                type="text"
                placeholder="YYYY-MM-DD [HH:MM]"
                value={group.startedAfter}
                onChange={(e) => group.setStartedAfter(e.target.value)}
                className={inputCls}
              />
            </label>
            <label className="block text-[10px] text-[var(--muted)]">
              before
              <input
                type="text"
                placeholder="YYYY-MM-DD [HH:MM]"
                value={group.startedBefore}
                onChange={(e) => group.setStartedBefore(e.target.value)}
                className={inputCls}
              />
            </label>
          </div>
          <div className="space-y-2 pt-2 border-t border-[var(--border)]">
            <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
              Finished
            </div>
            <label className="block text-[10px] text-[var(--muted)]">
              after
              <input
                type="text"
                placeholder="YYYY-MM-DD [HH:MM]"
                value={group.finishedAfter}
                onChange={(e) => group.setFinishedAfter(e.target.value)}
                className={inputCls}
              />
            </label>
            <label className="block text-[10px] text-[var(--muted)]">
              before
              <input
                type="text"
                placeholder="YYYY-MM-DD [HH:MM]"
                value={group.finishedBefore}
                onChange={(e) => group.setFinishedBefore(e.target.value)}
                className={inputCls}
              />
            </label>
          </div>
          {active && (
            <button
              onClick={() => {
                group.setStartedAfter("");
                group.setStartedBefore("");
                group.setFinishedAfter("");
                group.setFinishedBefore("");
              }}
              className="w-full text-left text-xs text-[var(--muted)] hover:text-[var(--foreground)] pt-2 border-t border-[var(--border)]"
            >
              clear all
            </button>
          )}
        </div>
      )}
    </div>
  );
}

// useFilterDropdownState provides the openDropdown state + outside-
// click + Escape handler shared by FullFilterBar consumers. The
// wrapping ref is returned so callers can attach it to their filter
// container element.
export function useFilterDropdownState() {
  const [openDropdown, setOpenDropdown] = useState<string | null>(null);
  const filterRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!openDropdown) return;
    const handler = (e: MouseEvent) => {
      if (!filterRef.current || filterRef.current.contains(e.target as Node))
        return;
      e.stopPropagation();
      e.preventDefault();
      setOpenDropdown(null);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpenDropdown(null);
    };
    document.addEventListener("click", handler, true);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("click", handler, true);
      document.removeEventListener("keydown", onKey);
    };
  }, [openDropdown]);
  return { openDropdown, setOpenDropdown, filterRef };
}
