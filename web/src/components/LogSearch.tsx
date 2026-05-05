"use client";

import { useState, useCallback } from "react";
import Link from "next/link";
import { type LogSearchResult, searchLogs } from "@/lib/api";

export default function LogSearch() {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<LogSearchResult[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [searched, setSearched] = useState(false);

  const doSearch = useCallback(async () => {
    if (query.length < 2) return;
    setLoading(true);
    try {
      const resp = await searchLogs(query, { limit: 50 });
      setResults(resp.results || []);
      setTotal(resp.total || 0);
      setSearched(true);
    } finally {
      setLoading(false);
    }
  }, [query]);

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") doSearch();
  };

  // Group results by job ID for compact display
  const grouped = new Map<string, LogSearchResult[]>();
  for (const r of results) {
    const existing = grouped.get(r.run_id);
    if (existing) {
      existing.push(r);
    } else {
      grouped.set(r.run_id, [r]);
    }
  }

  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-4">
      <div className="text-xs font-medium text-[var(--muted)] mb-3">
        Log Search
      </div>

      {/* Search input */}
      <div className="flex gap-2 mb-3">
        <input
          type="text"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder="Search across all job logs..."
          className="flex-1 bg-[var(--background)] border border-[var(--border)] rounded px-3 py-1.5 text-sm text-[var(--foreground)] placeholder:text-[var(--muted)] focus:outline-none focus:border-indigo-500/50"
        />
        <button
          onClick={doSearch}
          disabled={loading || query.length < 2}
          className="px-3 py-1.5 bg-indigo-500/20 border border-indigo-500/30 rounded text-xs text-indigo-300 hover:bg-indigo-500/30 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
        >
          {loading ? "..." : "Search"}
        </button>
      </div>

      {/* Results */}
      {searched && results.length === 0 && (
        <div className="text-xs text-[var(--muted)] py-4 text-center">
          No results found for &ldquo;{query}&rdquo;
        </div>
      )}

      {results.length > 0 && (
        <div className="space-y-3 max-h-96 overflow-y-auto">
          <div className="text-[10px] text-[var(--muted)]">
            {total} match{total !== 1 ? "es" : ""} across {grouped.size} job
            {grouped.size !== 1 ? "s" : ""}
          </div>

          {Array.from(grouped.entries()).map(([jobId, matches]) => (
            <div
              key={jobId}
              className="border border-[var(--border)] rounded bg-[var(--background)]"
            >
              <div className="flex items-center gap-2 px-3 py-1.5 border-b border-[var(--border)]">
                <Link
                  href={`/runs?job=${jobId}`}
                  className="text-xs font-mono text-violet-300 hover:text-violet-200 transition-colors"
                >
                  {jobId}
                </Link>
                <span className="text-[10px] text-[var(--muted)]">
                  {matches.length} match{matches.length !== 1 ? "es" : ""}
                </span>
              </div>
              <div className="divide-y divide-[var(--border)]">
                {matches.slice(0, 5).map((m, i) => (
                  <div key={i} className="px-3 py-1 flex gap-2">
                    <span className="text-[10px] text-[var(--muted)] shrink-0 w-8 text-right font-mono">
                      L{m.line}
                    </span>
                    <span className="text-xs font-mono text-[var(--foreground)] break-all whitespace-pre-wrap">
                      {highlightMatch(m.content, query)}
                    </span>
                  </div>
                ))}
                {matches.length > 5 && (
                  <div className="px-3 py-1 text-[10px] text-[var(--muted)]">
                    +{matches.length - 5} more
                  </div>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// Highlight matching text in a log line
function highlightMatch(content: string, query: string): React.ReactNode {
  const lowerContent = content.toLowerCase();
  const lowerQuery = query.toLowerCase();
  const idx = lowerContent.indexOf(lowerQuery);
  if (idx === -1) return content;

  const before = content.slice(0, idx);
  const match = content.slice(idx, idx + query.length);
  const after = content.slice(idx + query.length);

  return (
    <>
      {before}
      <span className="bg-amber-400/30 text-amber-200 rounded-sm px-0.5">
        {match}
      </span>
      {after}
    </>
  );
}
