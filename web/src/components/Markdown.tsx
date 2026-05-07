"use client";

import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

export default function Markdown({ content }: { content: string }) {
  return (
    <div className="prose prose-invert max-w-none px-8 py-6">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          h1: ({ children }) => (
            <h1 className="text-3xl font-bold mb-6 pb-2 border-b border-[var(--border)]">
              {children}
            </h1>
         ,
          h2: ({ children }) => (
            <h2 className="text-xl font-bold mt-8 mb-4">{children}</h2>
         ,
          h3: ({ children }) => (
            <h3 className="text-lg font-semibold mt-6 mb-3">{children}</h3>
         ,
          p: ({ children }) => (
            <p className="mb-4 leading-relaxed text-[var(--muted)]">
              {children}
            </p>
         ,
          a: ({ href, children }) => (
            <a
              href={href}
              className="text-indigo-400 hover:text-indigo-300 underline"
            >
              {children}
            </a>
         ,
          code: ({ className, children }) => {
            const isBlock = className?.includes("language-");
            if (isBlock) {
              return (
                <code
                  className={`block bg-[var(--surface)] border border-[var(--border)] rounded-lg p-4 text-sm font-mono overflow-x-auto whitespace-pre ${className}`}
                >
                  {children}
                </code>
              );
            }
            return (
              <code className="bg-[var(--surface)] border border-[var(--border)] px-1.5 py-0.5 rounded text-sm font-mono text-indigo-300">
                {children}
              </code>
            );
          },
          pre: ({ children }) => <pre className="mb-4">{children}</pre>,
          ul: ({ children }) => (
            <ul className="mb-4 ml-4 list-disc text-[var(--muted)]">
              {children}
            </ul>
         ,
          ol: ({ children }) => (
            <ol className="mb-4 ml-4 list-decimal text-[var(--muted)]">
              {children}
            </ol>
         ,
          li: ({ children }) => <li className="mb-1">{children}</li>,
          table: ({ children }) => (
            <div className="overflow-x-auto mb-4">
              <table className="w-full text-sm border-collapse border border-[var(--border)]">
                {children}
              </table>
            </div>
         ,
          thead: ({ children }) => (
            <thead className="bg-[var(--surface)]">{children}</thead>
         ,
          th: ({ children }) => (
            <th className="px-3 py-2 text-left font-semibold border border-[var(--border)]">
              {children}
            </th>
         ,
          td: ({ children }) => (
            <td className="px-3 py-2 border border-[var(--border)] text-[var(--muted)]">
              {children}
            </td>
         ,
          blockquote: ({ children }) => (
            <blockquote className="border-l-2 border-indigo-500 pl-4 mb-4 text-[var(--muted)] italic">
              {children}
            </blockquote>
         ,
          hr: () => <hr className="my-8 border-[var(--border)]" />,
          strong: ({ children }) => (
            <strong className="font-semibold text-[var(--foreground)]">
              {children}
            </strong>
         ,
        }}
      >
        {content}
      </ReactMarkdown>
    </div>
  );
}
