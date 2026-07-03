import { useEffect, useState } from "react";
import { highlightCode, resolveLanguage } from "@/lib/shiki";

export function CodePreview({
  content,
  fileName,
}: {
  content: string;
  fileName: string;
}) {
  const [html, setHtml] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setHtml(null);
    const lang = resolveLanguage(fileName);
    highlightCode(content, lang, { showLineNumbers: true })
      .then((h) => {
        if (!cancelled) setHtml(h);
      })
      .catch(() => {
        if (!cancelled) setHtml(null);
      });
    return () => {
      cancelled = true;
    };
  }, [content, fileName]);

  if (html) {
    return (
      <div
        className="shiki-host"
        dangerouslySetInnerHTML={{ __html: html }}
      />
    );
  }

  return (
    <pre className="p-3 font-mono text-[12px] leading-[1.6] text-ink whitespace-pre">
      {content}
    </pre>
  );
}
