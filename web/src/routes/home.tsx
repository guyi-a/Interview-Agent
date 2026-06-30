import { useNavigate } from "react-router";
import { PromptInput } from "@/features/chat/PromptInput";

export function Home() {
  const navigate = useNavigate();

  const onSend = (text: string) => {
    const id = crypto.randomUUID();
    navigate(`/c/${id}`, { state: { pending: text } });
  };

  return (
    <>
      <div className="flex-1 flex items-center justify-center">
        <div className="max-w-md text-center px-8">
          <div className="font-mono text-[10px] tracking-[0.2em] uppercase text-muted mb-4">
            Interview · Practice · Transcript
          </div>
          <h2 className="text-2xl mb-3">开始一次面试演练</h2>
          <p className="text-sm text-muted leading-relaxed">
            在下方输入第一句以开始；或从左侧打开已有会话。
            模型的推理过程会作为边注呈现。
          </p>
        </div>
      </div>
      <PromptInput streaming={false} onSend={onSend} onCancel={() => {}} />
    </>
  );
}
