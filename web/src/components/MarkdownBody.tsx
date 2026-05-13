"use client";

// MarkdownBody renders user-emitted markdown (sparkwing.Summary)
// inside a tight container — no page-level padding or oversized
// headings. Use this for embedded summary cards; the full-bleed
// Markdown component is for standalone docs.

import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

export default function MarkdownBody({ md }: { md: string }) {
  return (
    <div className="prose prose-invert prose-sm max-w-none">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          h1: ({ children }) => (
            <h1 className="text-base font-bold mt-0 mb-2 text-[var(--foreground)]">
              {children}
            </h1>
          ),
          h2: ({ children }) => (
            <h2 className="text-sm font-bold mt-3 mb-1.5 text-[var(--foreground)]">
              {children}
            </h2>
          ),
          h3: ({ children }) => (
            <h3 className="text-sm font-semibold mt-2 mb-1 text-[var(--foreground)]">
              {children}
            </h3>
          ),
          p: ({ children }) => (
            <p className="my-1 leading-snug text-[var(--foreground)]">
              {children}
            </p>
          ),
          a: ({ href, children }) => (
            <a
              href={href}
              target="_blank"
              rel="noopener noreferrer"
              className="text-cyan-400 hover:text-cyan-300 underline"
            >
              {children}
            </a>
          ),
          code: ({ className, children }) => {
            const isBlock = className?.includes("language-");
            if (isBlock) {
              return (
                <code
                  className={`block bg-[var(--surface)] border border-[var(--border)] rounded p-2 text-[11px] font-mono overflow-x-auto whitespace-pre my-1 ${className}`}
                >
                  {children}
                </code>
              );
            }
            return (
              <code className="bg-[var(--surface)] px-1 rounded text-[11px] font-mono text-indigo-300">
                {children}
              </code>
            );
          },
          pre: ({ children }) => <pre className="my-1">{children}</pre>,
          ul: ({ children }) => (
            <ul className="my-1 ml-4 list-disc text-[var(--foreground)]">
              {children}
            </ul>
          ),
          ol: ({ children }) => (
            <ol className="my-1 ml-4 list-decimal text-[var(--foreground)]">
              {children}
            </ol>
          ),
          li: ({ children }) => <li className="my-0">{children}</li>,
          table: ({ children }) => (
            <div className="overflow-x-auto my-2">
              <table className="text-[11px] border-collapse border border-[var(--border)]">
                {children}
              </table>
            </div>
          ),
          thead: ({ children }) => (
            <thead className="bg-[var(--surface)]">{children}</thead>
          ),
          th: ({ children }) => (
            <th className="px-2 py-1 text-left font-semibold border border-[var(--border)]">
              {children}
            </th>
          ),
          td: ({ children }) => (
            <td className="px-2 py-1 border border-[var(--border)] text-[var(--muted)]">
              {children}
            </td>
          ),
          blockquote: ({ children }) => (
            <blockquote className="border-l-2 border-cyan-500 pl-3 my-1 text-[var(--muted)] italic">
              {children}
            </blockquote>
          ),
          hr: () => <hr className="my-2 border-[var(--border)]" />,
          strong: ({ children }) => (
            <strong className="font-semibold text-[var(--foreground)]">
              {children}
            </strong>
          ),
        }}
      >
        {md}
      </ReactMarkdown>
    </div>
  );
}
