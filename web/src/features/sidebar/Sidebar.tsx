import { useEffect } from "react";
import { useNavigate } from "react-router";
import { useConversationStore } from "@/stores/conversations";
import { ConversationItem } from "./ConversationItem";

export function Sidebar() {
  const navigate = useNavigate();
  const items = useConversationStore((s) => s.items);
  const loading = useConversationStore((s) => s.loading);
  const refresh = useConversationStore((s) => s.refresh);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const onNew = () => {
    navigate("/");
  };

  return (
    <aside className="w-64 shrink-0 border-r border-rule flex flex-col bg-subtle/30">
      <div className="px-4 pt-5 pb-3">
        <h1 className="font-mono text-[10px] tracking-[0.2em] uppercase text-muted">
          Interview Agent
        </h1>
      </div>

      <button
        type="button"
        onClick={onNew}
        className="mx-3 mb-4 px-3 py-2 text-left text-sm border border-rule hover:border-ink hover:bg-paper transition-colors cursor-pointer"
      >
        <span className="font-mono text-[10px] tracking-[0.15em] uppercase text-muted mr-2">
          New
        </span>
        <span>新建会话</span>
      </button>

      <nav className="flex-1 overflow-y-auto px-1">
        {loading && items.length === 0 ? (
          <p className="px-3 py-2 text-xs text-muted">加载中…</p>
        ) : items.length === 0 ? (
          <p className="px-3 py-2 text-xs text-muted">还没有会话</p>
        ) : (
          <ul>
            {items.map((c) => (
              <ConversationItem key={c.id} item={c} />
            ))}
          </ul>
        )}
      </nav>
    </aside>
  );
}
