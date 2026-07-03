export function TextRenderer({ content }: { content: string }) {
  return (
    <pre className="px-4 py-4 font-mono text-[12px] leading-relaxed text-ink whitespace-pre-wrap break-words">
      {content}
    </pre>
  );
}
