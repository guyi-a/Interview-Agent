import { useCallback, useEffect, useRef, useState } from "react";
import {
  cancelChat,
  listMessages,
  postChat,
  type PersistedMessage,
} from "@/lib/api";

export type ToolCall = {
  id: string;
  name: string;
  argsJson: string;
  status: "running" | "ok" | "error";
  content?: string;
  error?: string;
};

export type ChatTurn = {
  id: string;
  role: "user" | "assistant";
  content: string;
  reasoning: string;
  tools: ToolCall[];
  createdAt: string;
  done: boolean;
  error?: string;
};

type Frame = {
  type:
    | "text"
    | "thinking"
    | "tool_call"
    | "tool_result"
    | "project_bound"
    | "usage"
    | "done"
    | "error";
  // Common
  content?: string;
  message?: string;
  // Tool
  id?: string;
  name?: string;
  args_json?: string;
  ok?: boolean;
  error?: string;
  // Project (PR B, ignored for now if it ever shows up early)
  project_id?: string;
  project_name?: string;
  workspace_path?: string;
};

function parseFrames(buffer: string): { frames: Frame[]; rest: string } {
  const frames: Frame[] = [];
  let rest = buffer;
  while (true) {
    const idx = rest.indexOf("\n\n");
    if (idx < 0) break;
    const block = rest.slice(0, idx);
    rest = rest.slice(idx + 2);
    const dataLines: string[] = [];
    for (const line of block.split("\n")) {
      if (line.startsWith("data:")) {
        dataLines.push(line.slice(5).trimStart());
      }
    }
    if (dataLines.length === 0) continue;
    try {
      frames.push(JSON.parse(dataLines.join("\n")) as Frame);
    } catch (err) {
      console.warn("[sse] bad frame", err, dataLines);
    }
  }
  return { frames, rest };
}

function fromPersisted(rows: PersistedMessage[]): ChatTurn[] {
  return rows
    .filter((r) => r.role === "user" || r.role === "assistant")
    .map((r) => ({
      id: `db-${r.seq}`,
      role: r.role as "user" | "assistant",
      content: r.content,
      reasoning: r.reasoning_content ?? "",
      tools: [],
      createdAt: r.created_at,
      done: true,
    }));
}

export function useChatStream(conversationID: string) {
  const [turns, setTurns] = useState<ChatTurn[]>([]);
  const [loading, setLoading] = useState(true);
  const [streaming, setStreaming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    let cancelled = false;
    setTurns([]);
    setLoading(true);
    setError(null);
    listMessages(conversationID)
      .then((rows) => {
        if (cancelled) return;
        setTurns(fromPersisted(rows));
      })
      .catch((err) => {
        if (cancelled) return;
        console.error("[chat] load history failed:", err);
        setError(String(err));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
      abortRef.current?.abort();
      abortRef.current = null;
    };
  }, [conversationID]);

  const send = useCallback(
    async (text: string) => {
      if (streaming) return;
      const trimmed = text.trim();
      if (!trimmed) return;

      const nowIso = new Date().toISOString();
      const userTurn: ChatTurn = {
        id: `u-${nowIso}`,
        role: "user",
        content: trimmed,
        reasoning: "",
        tools: [],
        createdAt: nowIso,
        done: true,
      };
      const assistantTurn: ChatTurn = {
        id: `a-${nowIso}`,
        role: "assistant",
        content: "",
        reasoning: "",
        tools: [],
        createdAt: nowIso,
        done: false,
      };
      setTurns((prev) => [...prev, userTurn, assistantTurn]);
      setStreaming(true);
      setError(null);

      const controller = new AbortController();
      abortRef.current = controller;

      const updateAssistant = (fn: (t: ChatTurn) => ChatTurn) => {
        setTurns((prev) => {
          const next = prev.slice();
          for (let i = next.length - 1; i >= 0; i--) {
            if (next[i].id === assistantTurn.id) {
              next[i] = fn(next[i]);
              break;
            }
          }
          return next;
        });
      };

      const upsertTool = (id: string, patch: Partial<ToolCall>) => {
        updateAssistant((t) => {
          const idx = t.tools.findIndex((tc) => tc.id === id);
          if (idx < 0) {
            const next: ToolCall = {
              id,
              name: patch.name ?? "",
              argsJson: patch.argsJson ?? "",
              status: patch.status ?? "running",
              content: patch.content,
              error: patch.error,
            };
            return { ...t, tools: [...t.tools, next] };
          }
          const merged = { ...t.tools[idx], ...patch };
          const tools = t.tools.slice();
          tools[idx] = merged;
          return { ...t, tools };
        });
      };

      try {
        const res = await postChat(conversationID, trimmed, controller.signal);
        if (!res.ok || !res.body) {
          throw new Error(`POST /chat: ${res.status}`);
        }
        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        let buf = "";
        let finished = false;
        while (!finished) {
          const { done, value } = await reader.read();
          if (done) break;
          buf += decoder.decode(value, { stream: true });
          const { frames, rest } = parseFrames(buf);
          buf = rest;
          for (const f of frames) {
            switch (f.type) {
              case "text":
                if (f.content)
                  updateAssistant((t) => ({
                    ...t,
                    content: t.content + f.content,
                  }));
                break;
              case "thinking":
                if (f.content)
                  updateAssistant((t) => ({
                    ...t,
                    reasoning: t.reasoning + f.content,
                  }));
                break;
              case "tool_call":
                if (f.id) {
                  upsertTool(f.id, {
                    name: f.name ?? "",
                    argsJson: f.args_json ?? "",
                    status: "running",
                  });
                }
                break;
              case "tool_result":
                if (f.id) {
                  upsertTool(f.id, {
                    name: f.name ?? undefined,
                    status: f.ok ? "ok" : "error",
                    content: f.ok ? f.content : undefined,
                    error: f.ok ? undefined : f.error ?? f.message,
                  });
                }
                break;
              case "project_bound":
                // PR B handles this; ignore for PR A
                break;
              case "usage":
                break;
              case "done":
                updateAssistant((t) => ({ ...t, done: true }));
                finished = true;
                break;
              case "error":
                updateAssistant((t) => ({
                  ...t,
                  done: true,
                  error: f.message ?? "unknown error",
                }));
                setError(f.message ?? "unknown error");
                finished = true;
                break;
            }
            if (finished) break;
          }
        }
      } catch (err) {
        if ((err as { name?: string }).name === "AbortError") {
          updateAssistant((t) => ({ ...t, done: true, error: "已取消" }));
        } else {
          console.error("[chat] stream error:", err);
          const msg = String(err);
          updateAssistant((t) => ({ ...t, done: true, error: msg }));
          setError(msg);
        }
      } finally {
        setStreaming(false);
        abortRef.current = null;
      }
    },
    [conversationID, streaming],
  );

  const cancel = useCallback(async () => {
    abortRef.current?.abort();
    abortRef.current = null;
    await cancelChat(conversationID);
  }, [conversationID]);

  return { turns, loading, streaming, error, send, cancel };
}
