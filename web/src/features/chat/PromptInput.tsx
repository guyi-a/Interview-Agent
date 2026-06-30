import { useState, type KeyboardEvent } from "react";

export function PromptInput({
  streaming,
  onSend,
  onCancel,
}: {
  streaming: boolean;
  onSend: (text: string) => void;
  onCancel: () => void;
}) {
  const [text, setText] = useState("");

  const submit = () => {
    const t = text.trim();
    if (!t || streaming) return;
    onSend(t);
    setText("");
  };

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      submit();
    }
  };

  return (
    <div className="border-t border-rule">
      <div className="max-w-3xl mx-auto px-8 py-4">
        <div className="flex gap-3 items-end">
          <textarea
            rows={1}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={onKey}
            placeholder={streaming ? "正在响应…" : "输入你的回答  ·  Enter 发送  ·  Shift+Enter 换行"}
            disabled={streaming}
            className="flex-1 resize-none px-3 py-2 border border-rule focus:border-ink focus:outline-none bg-paper text-[15px] leading-7 disabled:opacity-50"
            style={{ minHeight: "44px", maxHeight: "200px" }}
          />
          {streaming ? (
            <button
              type="button"
              onClick={onCancel}
              className="px-4 py-2 border border-rule text-sm hover:border-ink cursor-pointer"
            >
              取消
            </button>
          ) : (
            <button
              type="button"
              onClick={submit}
              disabled={!text.trim()}
              className="px-4 py-2 bg-accent text-paper text-sm hover:bg-accent-hover disabled:opacity-40 disabled:cursor-not-allowed cursor-pointer transition-colors"
            >
              发送
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
