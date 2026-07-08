// hitl-input: 纯函数集，负责把 ask_user 工具的入参（可能是数组，也可能被 LLM
// 序列化成 JSON 字符串）容错解析成前端能安全渲染的结构。放在独立文件是因为
// 不带 React / DOM 依赖，可以在别处也复用；关键是防 UI 白屏 —— 如果 questions
// 传成字符串，直接 .map 会抛 TypeError，QuestionCard 就崩了。

export type RawQuestion = {
  id?: string;
  label?: string;
  question: string;
  options: string[];
  multi_select?: boolean;
};

export type NormalizedOption = {
  label: string;       // 已抠掉 "(Recommended)" / "（推荐）" 后缀
  recommended: boolean;
};

export type NormalizedQuestion = {
  id: string;
  label: string;
  question: string;
  options: NormalizedOption[];
  multiSelect: boolean;
};

// 四种推荐后缀写法都认，UI 层识别后抠掉展示文本、右侧显示徽章、并把带推荐项排到前面。
const RECOMMENDED_RE = /\s*(?:\((?:recommended|recommend)\)|（推荐）|\[recommended\]|【推荐】)\s*$/i;

function normalizeOption(raw: string): NormalizedOption {
  const label = raw.replace(RECOMMENDED_RE, "").trim() || raw;
  return { label, recommended: RECOMMENDED_RE.test(raw) };
}

// coerceStringArray 把可能是数组或 JSON 字符串的输入统一转成 string[]。
// LLM 有时把 options / questions 生成成 JSON 字符串，前端不容错直接 .map 会崩。
export function coerceStringArray(raw: unknown): string[] {
  if (Array.isArray(raw)) {
    return raw.filter((v): v is string => typeof v === "string");
  }
  if (typeof raw === "string") {
    const trimmed = raw.trim();
    if (trimmed.startsWith("[")) {
      try {
        const parsed = JSON.parse(trimmed) as unknown;
        if (Array.isArray(parsed)) {
          return parsed.filter((v): v is string => typeof v === "string");
        }
      } catch {
        // fall through
      }
    }
  }
  return [];
}

export function coerceQuestions(raw: unknown): RawQuestion[] {
  if (Array.isArray(raw)) return raw as RawQuestion[];
  if (typeof raw === "string") {
    const trimmed = raw.trim();
    if (trimmed.startsWith("[")) {
      try {
        const parsed = JSON.parse(trimmed) as unknown;
        if (Array.isArray(parsed)) return parsed as RawQuestion[];
      } catch {
        // fall through
      }
    }
  }
  return [];
}

// normalizeQuestions 把 raw 输入（可能是数组、JSON 字符串、或含单问题老结构）
// 归一成可渲染的 NormalizedQuestion 列表。推荐项自动排到前面。
export function normalizeQuestions(raw: unknown): NormalizedQuestion[] {
  const questions = coerceQuestions(raw);
  return questions
    .map((q, index): NormalizedQuestion | null => {
      const options = coerceStringArray(q.options)
        .map(normalizeOption)
        .sort((a, b) => Number(b.recommended) - Number(a.recommended));
      const questionText = typeof q.question === "string" ? q.question : "";
      if (!questionText || options.length === 0) return null;
      return {
        id: q.id || `q${index + 1}`,
        label: q.label || questionText,
        question: questionText,
        options,
        multiSelect: Boolean(q.multi_select),
      };
    })
    .filter((q): q is NormalizedQuestion => q !== null);
}

// parseQuestionsJson 是 SSE frame / GET pending 的 questions_json 字段的入口。
// 字段本身是 JSON 字符串，先 parse 再走 normalizeQuestions 兜底。空串或异常都
// 返回空数组，让 UI 平滑显示"无问题"而不是崩。
export function parseQuestionsJson(json: string | undefined): NormalizedQuestion[] {
  if (!json) return [];
  try {
    return normalizeQuestions(JSON.parse(json));
  } catch {
    return [];
  }
}
