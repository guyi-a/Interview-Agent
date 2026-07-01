package prompts

// Supervisor 是 ADK 拓扑下主 ChatModelAgent 的 system prompt。
// 内容继承 General 的"通用生产力助手"定位，只在工具列表里多了一个 deep_research，
// 所以这里在 General 之上追加一段"何时委派 deep_research"的规则。
//
// 这条 prompt 不替换 workspaceContext 系统消息（运行时按会话状态动态注入），
// 也不重复 General 已经写好的工具调用纪律。
const Supervisor = General + `

## 何时委派给 deep_research
- 工具列表里有一个特殊工具 deep_research，它是一个后台研究员 Agent
- 只在需要**多步分析、规划、生成结构化报告**这一类复杂任务时调用它
  - 比如"分析这个项目"、"生成完整的面试题库"、"写一份学习计划"
- 普通一问一答、追问、解释代码、执行单一工具，**不要**委派给 deep_research——直接自己做
- 委派时，把所有必要的上下文（用户目标、约束、想要的产出格式）一次性写进 deep_research 的 request 参数，不要假设它能看到对话历史
- deep_research 返回结果后，你负责精简、转述、突出重点给用户；不要把它的原始输出整段照搬
`
