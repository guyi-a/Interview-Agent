import { useState } from "react";
import { ConversationItem } from "./ConversationItem";
import { cn } from "@/lib/utils";
import type {
  ConversationItem as ConvItem,
  ProjectItem,
} from "@/lib/api";

export function ProjectGroup({
  project,
  conversations,
}: {
  project: ProjectItem;
  conversations: ConvItem[];
}) {
  const [open, setOpen] = useState(true);

  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="w-full px-3 py-1.5 flex items-center gap-1.5 text-left text-muted hover:text-ink cursor-pointer"
      >
        <span
          className={cn(
            "font-mono text-[9px] inline-block transition-transform",
            !open && "-rotate-90",
          )}
        >
          ▾
        </span>
        <span className="text-[12px] truncate">{project.name || project.id}</span>
        <span className="font-mono text-[10px] text-muted/70 shrink-0 ml-auto">
          {conversations.length}
        </span>
      </button>
      {open && (
        <ul>
          {conversations.length === 0 ? (
            <li className="px-3 pl-7 py-1 text-[11px] text-muted/70">无会话</li>
          ) : (
            conversations.map((c) => <ConversationItem key={c.id} item={c} indent />)
          )}
        </ul>
      )}
    </div>
  );
}
