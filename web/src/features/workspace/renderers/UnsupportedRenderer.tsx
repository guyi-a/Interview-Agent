import { workspaceDownloadURL } from "@/lib/api";

export function UnsupportedRenderer({
  conversationId,
  path,
  name,
  size,
  reason,
  projectId,
}: {
  conversationId: string;
  path: string;
  name: string;
  size: number;
  reason: string;
  projectId?: string;
}) {
  return (
    <div className="p-6 flex flex-col items-center justify-center gap-3 text-center">
      <div className="text-[13px] text-muted italic">{reason}</div>
      <div className="text-[12px] text-ink/70">
        {name} · {formatSize(size)}
      </div>
      <a
        href={workspaceDownloadURL(conversationId, path, { projectId })}
        download={name}
        className="mt-2 inline-flex items-center gap-1.5 px-3 py-1.5 border border-rule rounded text-[12px] text-ink hover:border-accent hover:text-accent transition-colors"
      >
        下载
      </a>
    </div>
  );
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(2)} MB`;
}
