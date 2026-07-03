import { useEffect, useRef } from "react";
import { TranscriptEntry } from "./TranscriptEntry";
import type { ChatTurn } from "@/hooks/useChatStream";

export function Transcript({
  turns,
  streaming,
}: {
  turns: ChatTurn[];
  streaming: boolean;
}) {
  const endRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [turns, streaming]);

  if (turns.length === 0) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <p className="text-sm text-muted">
          开始一次对话——下方输入你的第一个问题。
        </p>
      </div>
    );
  }

  return (
    <div className="flex-1 overflow-y-auto scrollbar-subtle">
      <div className="max-w-3xl mx-auto px-8 py-10">
        {turns.map((t, i) => (
          <TranscriptEntry
            key={t.id}
            turn={t}
            showRule={i > 0}
            streaming={streaming && i === turns.length - 1 && t.role === "assistant"}
          />
        ))}
        <div ref={endRef} />
      </div>
    </div>
  );
}
