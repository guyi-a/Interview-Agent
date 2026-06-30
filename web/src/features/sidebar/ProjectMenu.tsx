import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useProjectStore } from "@/stores/projects";
import type { ProjectItem } from "@/lib/api";

export function ProjectMenu({
  project,
  open,
  onOpenChange,
  onNewConversation,
  onRename,
  onDelete,
}: {
  project: ProjectItem;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  conversationCount: number;
  onNewConversation: () => void;
  onRename: () => void;
  onDelete: () => void;
}) {
  const openInFinder = useProjectStore((s) => s.openInFinder);

  return (
    <DropdownMenu open={open} onOpenChange={onOpenChange}>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label="项目操作"
          className="size-5 flex items-center justify-center text-muted hover:text-ink cursor-pointer"
          onClick={(e) => e.stopPropagation()}
        >
          ⋯
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem
          onSelect={(e) => {
            e.preventDefault();
            onNewConversation();
          }}
        >
          <span className="font-mono text-[10px] text-muted">+</span>
          新对话
        </DropdownMenuItem>
        <DropdownMenuItem
          onSelect={(e) => {
            e.preventDefault();
            openInFinder(project.id).catch(() => {});
          }}
        >
          <span className="font-mono text-[10px] text-muted">↗</span>
          在 Finder 中打开
        </DropdownMenuItem>
        <DropdownMenuItem
          onSelect={(e) => {
            e.preventDefault();
            onRename();
          }}
        >
          <span className="font-mono text-[10px] text-muted">✎</span>
          重命名
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          destructive
          onSelect={(e) => {
            e.preventDefault();
            onDelete();
          }}
        >
          <span className="font-mono text-[10px]">✕</span>
          删除项目
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
