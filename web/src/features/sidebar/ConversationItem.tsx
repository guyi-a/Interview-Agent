import { NavLink, useNavigate, useParams } from "react-router";
import { useConversationStore } from "@/stores/conversations";
import { cn, formatRelative } from "@/lib/utils";
import type { ConversationItem as ConvItem } from "@/lib/api";

export function ConversationItem({
  item,
  indent = false,
}: {
  item: ConvItem;
  indent?: boolean;
}) {
  const remove = useConversationStore((s) => s.remove);
  const navigate = useNavigate();
  const { id: activeId } = useParams();

  const onDelete = async (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    await remove(item.id);
    if (activeId === item.id) {
      navigate("/");
    }
  };

  return (
    <li>
      <NavLink
        to={`/c/${item.id}`}
        className={({ isActive }) =>
          cn(
            "group block py-1.5 border-l-2 transition-colors",
            indent ? "pl-7 pr-3" : "px-3",
            isActive
              ? "border-accent bg-paper"
              : "border-transparent hover:bg-paper",
          )
        }
      >
        <div className="flex items-center gap-2">
          <div className="flex-1 min-w-0">
            <div
              className={cn(
                "truncate text-ink",
                indent ? "text-[13px]" : "text-sm",
              )}
            >
              {item.title || "（未命名）"}
            </div>
            {!indent && (
              <div className="font-mono text-[10px] tracking-wider uppercase text-muted mt-0.5">
                {formatRelative(item.updated_at)}
              </div>
            )}
          </div>
          <button
            type="button"
            onClick={onDelete}
            aria-label="删除会话"
            className="opacity-0 group-hover:opacity-100 text-muted hover:text-ink text-xs px-1 cursor-pointer transition-opacity"
          >
            ✕
          </button>
        </div>
      </NavLink>
    </li>
  );
}
