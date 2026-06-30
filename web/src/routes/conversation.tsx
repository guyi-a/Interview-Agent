import { useCallback, useEffect, useRef } from "react";
import { useLocation, useParams } from "react-router";
import { useChatStream } from "@/hooks/useChatStream";
import { useConversationStore } from "@/stores/conversations";
import { useProjectStore } from "@/stores/projects";
import { Transcript } from "@/features/chat/Transcript";
import { PromptInput } from "@/features/chat/PromptInput";

export function Conversation() {
  const { id } = useParams();
  if (!id) return null;

  const location = useLocation();
  const state = location.state as
    | { pending?: string; projectId?: string }
    | null;
  const pending = state?.pending;
  const projectId = state?.projectId;

  const touch = useConversationStore((s) => s.touch);
  const refreshConvs = useConversationStore((s) => s.refresh);
  const refreshProjects = useProjectStore((s) => s.refresh);

  const onProjectBound = useCallback(() => {
    // Conversation just got bound to a project — refresh both stores so
    // the sidebar can immediately move this item from Ad-hoc to the new
    // project group, mid-stream.
    refreshConvs();
    refreshProjects();
  }, [refreshConvs, refreshProjects]);

  const { turns, loading, streaming, send, cancel } = useChatStream(id, {
    onProjectBound,
    projectId,
  });

  const onSend = async (text: string) => {
    touch(id, text.slice(0, 20), { projectId });
    await send(text);
    refreshConvs();
    refreshProjects();
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
