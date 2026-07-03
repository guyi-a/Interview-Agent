import { MessageBody } from "@/features/chat/MessageBody";

export function MarkdownRenderer({ content }: { content: string }) {
  return (
    <div className="px-4 py-4">
      <MessageBody content={content} />
    </div>
  );
}
