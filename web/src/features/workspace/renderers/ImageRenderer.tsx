import { workspaceDownloadURL } from "@/lib/api";

export function ImageRenderer({
  conversationId,
  path,
  name,
  projectId,
}: {
  conversationId: string;
  path: string;
  name: string;
  projectId?: string;
}) {
  const src = workspaceDownloadURL(conversationId, path, { projectId });
  return (
    <div className="flex h-full min-h-0 items-center justify-center overflow-auto bg-subtle p-6">
      <img
        src={src}
        alt={name}
        className="max-w-full max-h-full object-contain rounded"
      />
    </div>
  );
}
