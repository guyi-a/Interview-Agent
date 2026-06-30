import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { useConversationStore } from "@/stores/conversations";
import { useProjectStore } from "@/stores/projects";
import { ConversationItem } from "./ConversationItem";
import { ProjectGroup } from "./ProjectGroup";
import { cn } from "@/lib/utils";

export function Sidebar() {
  const navigate = useNavigate();
  const conversations = useConversationStore((s) => s.items);
  const convLoading = useConversationStore((s) => s.loading);
  const refreshConv = useConversationStore((s) => s.refresh);
  const projects = useProjectStore((s) => s.items);
  const refreshProjects = useProjectStore((s) => s.refresh);

  useEffect(() => {
    refreshConv();
    refreshProjects();
  }, [refreshConv, refreshProjects]);

  const [projectsOpen, setProjectsOpen] = useState(true);
  const [adhocOpen, setAdhocOpen] = useState(true);

  const { adhocConversations, byProject } = useMemo(() => {
    const adhoc: typeof conversations = [];
    const byPid = new Map<string, typeof conversations>();
    for (const c of conversations) {
      if (c.project_id) {
        const list = byPid.get(c.project_id) ?? [];
        list.push(c);
        byPid.set(c.project_id, list);
      } else {
        adhoc.push(c);
      }
    }
    return { adhocConversations: adhoc, byProject: byPid };
  }, [conversations]);

  const onNew = () => navigate("/");

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

      <nav className="flex-1 overflow-y-auto">
        {/* PROJECTS section */}
        <section className="mb-3">
          <button
            type="button"
            onClick={() => setProjectsOpen((v) => !v)}
            className="w-full px-3 py-1 flex items-center gap-1.5 text-left cursor-pointer"
          >
            <span
              className={cn(
                "font-mono text-[9px] inline-block text-muted transition-transform",
                !projectsOpen && "-rotate-90",
              )}
            >
              ▾
            </span>
            <span className="font-mono text-[10px] tracking-[0.18em] uppercase text-muted">
              Projects
            </span>
            <span className="font-mono text-[10px] text-muted/60 ml-auto">
              {projects.length}
            </span>
          </button>
          {projectsOpen && (
            <div>
              {projects.length === 0 ? (
                <p className="px-3 pl-7 py-1 text-[11px] text-muted/70">
                  还没有项目
                </p>
              ) : (
                projects.map((p) => (
                  <ProjectGroup
                    key={p.id}
                    project={p}
                    conversations={byProject.get(p.id) ?? []}
                  />
                ))
              )}
            </div>
          )}
        </section>

        {/* AD-HOC section */}
        <section>
          <button
            type="button"
            onClick={() => setAdhocOpen((v) => !v)}
            className="w-full px-3 py-1 flex items-center gap-1.5 text-left cursor-pointer"
          >
            <span
              className={cn(
                "font-mono text-[9px] inline-block text-muted transition-transform",
                !adhocOpen && "-rotate-90",
              )}
            >
              ▾
            </span>
            <span className="font-mono text-[10px] tracking-[0.18em] uppercase text-muted">
              Ad-hoc
            </span>
            <span className="font-mono text-[10px] text-muted/60 ml-auto">
              {adhocConversations.length}
            </span>
          </button>
          {adhocOpen && (
            <ul>
              {convLoading && conversations.length === 0 ? (
                <li className="px-3 py-2 text-xs text-muted">加载中…</li>
              ) : adhocConversations.length === 0 ? (
                <li className="px-3 py-2 text-[11px] text-muted/70">
                  没有 ad-hoc 会话
                </li>
              ) : (
                adhocConversations.map((c) => (
                  <ConversationItem key={c.id} item={c} />
                ))
              )}
            </ul>
          )}
        </section>
      </nav>
    </aside>
  );
}
