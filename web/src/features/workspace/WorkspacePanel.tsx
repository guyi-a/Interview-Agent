import { useCallback, useEffect, useRef, useState } from "react";
import { useParams } from "react-router";
import { useWorkspaceStore } from "./store";
import { WorkspaceTree } from "./WorkspaceTree";
import { FilePreview } from "./FilePreview";
import { cn } from "@/lib/utils";

export function WorkspacePanel({
  streaming,
  projectId,
}: {
  streaming: boolean;
  projectId?: string;
}) {
  const { id: conversationId } = useParams();
  const panelOpen = useWorkspaceStore((s) => s.panelOpen);
  const previewPath = useWorkspaceStore((s) => s.previewPath);
  const previewWidth = useWorkspaceStore((s) => s.previewWidth);
  const setPreviewWidth = useWorkspaceStore((s) => s.setPreviewWidth);
  const refreshFiles = useWorkspaceStore((s) => s.refreshFiles);
  const resetConversationState = useWorkspaceStore(
    (s) => s.resetConversationState,
  );
  const startXRef = useRef(0);
  const startWidthRef = useRef(previewWidth);
  const previousConversationIdRef = useRef<string | undefined>(conversationId);
  const [resizing, setResizing] = useState(false);

  useEffect(() => {
    if (!conversationId) return;
    if (previousConversationIdRef.current === conversationId) return;
    previousConversationIdRef.current = conversationId;
    resetConversationState();
  }, [conversationId, resetConversationState]);

  const onPointerDown = useCallback(
    (event: React.PointerEvent) => {
      if (!previewPath) return;
      event.preventDefault();
      startXRef.current = event.clientX;
      startWidthRef.current = previewWidth;
      setResizing(true);
    },
    [previewPath, previewWidth],
  );

  useEffect(() => {
    if (!resizing) return;

    const onPointerMove = (event: PointerEvent) => {
      const delta = startXRef.current - event.clientX;
      setPreviewWidth(startWidthRef.current + delta);
    };
    const onPointerUp = () => setResizing(false);

    window.addEventListener("pointermove", onPointerMove);
    window.addEventListener("pointerup", onPointerUp);
    return () => {
      window.removeEventListener("pointermove", onPointerMove);
      window.removeEventListener("pointerup", onPointerUp);
    };
  }, [resizing, setPreviewWidth]);

  useEffect(() => {
    if (!panelOpen || !conversationId || !streaming) return;
    refreshFiles();
    const interval = window.setInterval(refreshFiles, 2000);
    return () => window.clearInterval(interval);
  }, [conversationId, panelOpen, refreshFiles, streaming]);

  if (!panelOpen || !conversationId) return null;

  return (
    <aside
      className={cn(
        "relative shrink-0 flex flex-col min-h-0 p-3",
        !resizing && "transition-[width] duration-200 ease-out",
        resizing && "select-none",
      )}
      style={{ width: previewPath ? previewWidth : 320 }}
    >
      {previewPath && (
        <div
          role="separator"
          aria-orientation="vertical"
          aria-label="调整文件预览宽度"
          tabIndex={0}
          onPointerDown={onPointerDown}
          className={cn(
            "absolute left-0 top-0 z-20 h-full w-2 cursor-col-resize",
            "before:absolute before:left-3 before:top-3 before:bottom-3 before:w-px before:bg-rule/80",
            "after:absolute after:left-2 after:top-3 after:bottom-3 after:w-1 after:rounded-full after:bg-transparent hover:after:bg-accent/20",
          )}
        />
      )}
      <div className="flex-1 min-h-0 flex flex-col rounded-lg border border-rule bg-paper overflow-hidden">
        {previewPath ? (
          <FilePreview
            conversationId={conversationId}
            path={previewPath}
            projectId={projectId}
          />
        ) : (
          <WorkspaceTree conversationId={conversationId} projectId={projectId} />
        )}
      </div>
    </aside>
  );
}
