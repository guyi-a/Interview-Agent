import { useEffect, useRef } from "react";
import { useLocation, useParams } from "react-router";
import { useChatStream } from "@/hooks/useChatStream";
import { useConversationStore } from "@/stores/conversations";
import { Transcript } from "@/features/chat/Transcript";
import { PromptInput } from "@/features/chat/PromptInput";

export function Conversation() {
  const { id } = useParams();
  if (!id) return null;

  const location = useLocation();
  const pending = (location.state as { pending?: string } | null)?.pending;

  const touch = useConversationStore((s) => s.touch);
  const refresh = useConversationStore((s) => s.refresh);
  const { turns, loading, streaming, send, cancel } = useChatStream(id);

  const onSend = async (text: string) => {
    touch(id, text.slice(0, 20));
    await send(text);
    refresh();
  };

  const pendingFiredRef = useRef(false);
  useEffect(() => {
    if (loading) return;
    if (!pending) return;
    if (pendingFiredRef.current) return;
    pendingFiredRef.current = true;
    window.history.replaceState({}, "");
    onSend(pending);
  }, [loading, pending]);

  if (loading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <p className="text-sm text-muted">加载会话…</p>
      </div>
    );
  }

  return (
    <>
      <Transcript turns={turns} streaming={streaming} />
      <PromptInput streaming={streaming} onSend={onSend} onCancel={cancel} />
    </>
  );
}
