import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { ConversationItem } from "./ConversationItem";
import { ProjectMenu } from "./ProjectMenu";
import { useConversationStore } from "@/stores/conversations";
import { useProjectStore } from "@/stores/projects";
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
  const [menuOpen, setMenuOpen] = useState(false);
  const [renameOpen, setRenameOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const navigate = useNavigate();
  const { id: activeId } = useParams();
  const refreshConvs = useConversationStore((s) => s.refresh);
  const removeProject = useProjectStore((s) => s.remove);

  const onNewConversation = () => {
    const id = crypto.randomUUID();
    navigate(`/c/${id}`, { state: { projectId: project.id } });
  };

  const onConfirmDelete = async () => {
    setDeleteOpen(false);
    const warning = await removeProject(project.id).catch((e) => {
      console.error(e);
      return undefined;
    });
    if (warning) {
      console.warn("project deleted with warning:", warning);
    }
    await refreshConvs();
    // If the active conversation was inside this project, route home.
    const wasInside = conversations.some((c) => c.id === activeId);
    if (wasInside) navigate("/");
  };

  return (
    <div className="group/project">
      <div className="px-3 py-1.5 flex items-center gap-1.5">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className="flex items-center gap-1.5 flex-1 min-w-0 text-left text-muted hover:text-ink cursor-pointer"
        >
          <span
            className={cn(
              "font-mono text-[9px] inline-block transition-transform shrink-0",
              !open && "-rotate-90",
            )}
          >
            ▾
          </span>
          <span className="text-[12px] truncate">
            {project.name || project.id}
          </span>
        </button>
        <span
          className={cn(
            "font-mono text-[10px] text-muted/70 shrink-0",
            menuOpen ? "hidden" : "group-hover/project:hidden",
          )}
        >
          {conversations.length}
        </span>
        <div className={cn("hidden", menuOpen ? "flex" : "group-hover/project:flex")}>
          <ProjectMenu
            project={project}
            open={menuOpen}
            onOpenChange={setMenuOpen}
            conversationCount={conversations.length}
            onNewConversation={onNewConversation}
            onRename={() => setRenameOpen(true)}
            onDelete={() => setDeleteOpen(true)}
          />
        </div>
      </div>

      {open && (
        <ul>
          {conversations.length === 0 ? (
            <li className="px-3 pl-7 py-1 text-[11px] text-muted/70">
              无会话
            </li>
          ) : (
            conversations.map((c) => (
              <ConversationItem key={c.id} item={c} indent />
            ))
          )}
        </ul>
      )}

      <RenameDialog
        open={renameOpen}
        onOpenChange={setRenameOpen}
        project={project}
      />
      <DeleteDialog
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        project={project}
        conversationCount={conversations.length}
        onConfirm={onConfirmDelete}
      />
    </div>
  );
}

// --- Rename dialog ---

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";

function RenameDialog({
  open,
  onOpenChange,
  project,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  project: ProjectItem;
}) {
  const rename = useProjectStore((s) => s.rename);
  const [value, setValue] = useState(project.name);
  const [pending, setPending] = useState(false);

  // Reset input when dialog opens with a different project.
  if (open && !pending && value !== project.name && !value) {
    setValue(project.name);
  }

  const submit = async () => {
    const v = value.trim();
    if (!v || v === project.name) {
      onOpenChange(false);
      return;
    }
    setPending(true);
    try {
      await rename(project.id, v);
      onOpenChange(false);
    } finally {
      setPending(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(v) => { onOpenChange(v); if (v) setValue(project.name); }}>
      <DialogContent aria-describedby="rename-desc">
        <DialogTitle>Rename project</DialogTitle>
        <DialogDescription id="rename-desc">
          仅修改项目展示名，slug（{project.id}）和工作区路径不变。
        </DialogDescription>
        <input
          type="text"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") submit();
            if (e.key === "Escape") onOpenChange(false);
          }}
          autoFocus
          className="mt-4 w-full px-3 py-2 border border-rule focus:border-ink focus:outline-none bg-paper text-[14px]"
          placeholder="项目名"
        />
        <div className="mt-4 flex justify-end gap-2">
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            className="px-3 py-1.5 text-[13px] border border-rule hover:border-ink cursor-pointer"
          >
            取消
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={pending || !value.trim()}
            className="px-3 py-1.5 text-[13px] bg-accent text-paper hover:bg-accent-hover disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
          >
            {pending ? "保存中…" : "保存"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

// --- Delete confirm dialog ---

function DeleteDialog({
  open,
  onOpenChange,
  project,
  conversationCount,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  project: ProjectItem;
  conversationCount: number;
  onConfirm: () => Promise<void>;
}) {
  const [pending, setPending] = useState(false);

  const submit = async () => {
    setPending(true);
    try {
      await onConfirm();
    } finally {
      setPending(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent aria-describedby="del-desc">
        <DialogTitle>Delete project</DialogTitle>
        <DialogDescription id="del-desc">
          将永久删除项目「<span className="text-ink">{project.name || project.id}</span>」、
          其下 {conversationCount} 段会话与全部消息，以及磁盘上的工作区目录
          <span className="font-mono text-[11px] text-ink"> {project.workspace}</span>。
          此操作不可撤销。
        </DialogDescription>
        <div className="mt-5 flex justify-end gap-2">
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            className="px-3 py-1.5 text-[13px] border border-rule hover:border-ink cursor-pointer"
          >
            取消
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={pending}
            className="px-3 py-1.5 text-[13px] bg-red-600 text-paper hover:bg-red-700 disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
          >
            {pending ? "删除中…" : "确认删除"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
