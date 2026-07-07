// markdown.go：面试题库这类 "## 章节 / ### 问题" 结构 markdown 的语义化切分。
//
// 流程：
//   1. 剥 YAML frontmatter 和 <div>...</div> HTML 装饰块
//   2. 按 ### 切成 Q&A section（追踪 ## 作为 chapter context）
//   3. 每个 chunk 前缀 "章节：... / 问题：... / 答案：..." 便于 embedding 命中
//   4. section body 超长 → 用底层 Splitter 内部切，所有子片共享同一前缀（"答案片段："）
//
// CharStart/CharEnd 说明：指向**剥完 frontmatter/HTML 后**文本的 rune 偏移，
// 不是原始文件位置，也不严格等于 Content（Content 带合成前缀）。
package chunker

import (
	"strings"
	"unicode/utf8"
)

type MarkdownSplitter struct {
	Size    int
	Overlap int
	body    *Splitter
}

func NewMarkdown(size, overlap int) *MarkdownSplitter {
	return &MarkdownSplitter{
		Size:    size,
		Overlap: overlap,
		body:    New(size, overlap),
	}
}

func (m *MarkdownSplitter) Split(text string) []Chunk {
	text = stripFrontmatter(text)
	text = stripHTMLDivs(text)
	sections := extractQASections(text)

	var out []Chunk
	ord := 0
	for _, sec := range sections {
		body := strings.TrimSpace(sec.Body)
		if body == "" {
			continue
		}
		bodyRunes := []rune(body)
		prefixOne := renderPrefix(sec, false)
		if len(bodyRunes)+utf8.RuneCountInString(prefixOne) <= m.Size {
			out = append(out, Chunk{
				Ord:       ord,
				Content:   prefixOne + body,
				CharStart: sec.BodyStart,
				CharEnd:   sec.BodyStart + len(bodyRunes),
			})
			ord++
			continue
		}
		// 超长 → 先按 fenced code block 切成 text/code 段，每段独立处理：
		//   text 段 → 底层 Splitter 递归切
		//   code 段 → 整块保留（即便超 Size 也不切，保代码语法完整）
		// 所有子片带同一 "答案片段：" 前缀。
		prefixFrag := renderPrefix(sec, true)
		for _, seg := range splitByFences(body) {
			if seg.kind == segCode {
				segLen := utf8.RuneCountInString(seg.text)
				out = append(out, Chunk{
					Ord:       ord,
					Content:   prefixFrag + seg.text,
					CharStart: sec.BodyStart + seg.bodyOff,
					CharEnd:   sec.BodyStart + seg.bodyOff + segLen,
				})
				ord++
				continue
			}
			for _, sub := range m.body.Split(seg.text) {
				out = append(out, Chunk{
					Ord:       ord,
					Content:   prefixFrag + sub.Content,
					CharStart: sec.BodyStart + seg.bodyOff + sub.CharStart,
					CharEnd:   sec.BodyStart + seg.bodyOff + sub.CharEnd,
				})
				ord++
			}
		}
	}
	return out
}

// --- fenced code block protection ---------------------------------------

const (
	segText = "text"
	segCode = "code"
)

type mdSegment struct {
	kind    string // segText | segCode
	text    string
	bodyOff int // rune 偏移（在 body 内）
}

// splitByFences 把 body 切成 text/code 交替段。围栏仅识别行首 ``` (三反引号)，
// 不匹配行内反引号；未闭合的围栏视作从起点到 body 末尾的一整块 code（保完整）。
func splitByFences(body string) []mdSegment {
	lines := strings.Split(body, "\n")
	// 每行行首 rune 偏移
	lineOff := make([]int, len(lines)+1)
	off := 0
	for i, l := range lines {
		lineOff[i] = off
		off += utf8.RuneCountInString(l) + 1 // \n
	}
	lineOff[len(lines)] = off

	var segs []mdSegment
	var curLines []string
	curStart := 0
	curKind := segText
	inCode := false

	// mdSegment 不存 end，用 bodyOff + utf8.RuneCountInString(text) 恢复即可，
	// 所以 flush 不再需要 endOff 参数。
	flush := func() {
		if len(curLines) == 0 {
			return
		}
		text := strings.Join(curLines, "\n")
		if curKind == segText && strings.TrimSpace(text) == "" {
			curLines = nil
			return
		}
		if curKind == segCode {
			text = strings.TrimRight(text, "\n")
		}
		segs = append(segs, mdSegment{kind: curKind, text: text, bodyOff: curStart})
		curLines = nil
	}

	for i, line := range lines {
		if strings.HasPrefix(line, "```") {
			if !inCode {
				flush()
				curStart = lineOff[i]
				curKind = segCode
				curLines = []string{line}
				inCode = true
				continue
			}
			curLines = append(curLines, line)
			flush()
			if i+1 < len(lines) {
				curStart = lineOff[i+1]
			} else {
				curStart = lineOff[len(lines)]
			}
			curKind = segText
			inCode = false
			continue
		}
		curLines = append(curLines, line)
	}
	// EOF：未闭合围栏时，当前累积仍作 code 段一整块保完整。
	flush()
	return segs
}

func stripFrontmatter(text string) string {
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return text
	}
	// 找到闭合 "---" 行
	rest := text
	sep := "\n---\n"
	i := strings.Index(rest[3:], sep)
	if i < 0 {
		// 尝试 EOF 处的 \n---
		if strings.HasSuffix(rest, "\n---") {
			return ""
		}
		return text
	}
	return rest[3+i+len(sep):]
}

// stripHTMLDivs 深度感知地移除 <div ...>...</div> 块（支持嵌套）。
// 正则做不到嵌套匹配，且面试文档里的推广块外 div 内嵌了对齐 div，
// 非嵌套感知的实现会在内层 </div> 就误关闭。
func stripHTMLDivs(text string) string {
	var b strings.Builder
	i := 0
	for i < len(text) {
		open := indexAtStart(text[i:], "<div")
		if open < 0 {
			b.WriteString(text[i:])
			break
		}
		b.WriteString(text[i : i+open])
		i += open
		// 从 i 开始按 <div/</div> 计数找匹配闭合
		depth := 0
		j := i
		for j < len(text) {
			if hasPrefixAt(text, j, "<div") {
				depth++
				j += 4
				continue
			}
			if hasPrefixAt(text, j, "</div>") {
				depth--
				j += 6
				if depth == 0 {
					break
				}
				continue
			}
			j++
		}
		if depth != 0 {
			// 未闭合：保守起见把剩余原文吐出，不硬吞
			b.WriteString(text[i:])
			return b.String()
		}
		i = j
		// 顺手吃掉紧跟的空白行，避免 chunk 里空一大段
		for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
			i++
		}
		if i < len(text) && text[i] == '\n' {
			i++
		}
	}
	return b.String()
}

func indexAtStart(s, sub string) int  { return strings.Index(s, sub) }
func hasPrefixAt(s string, i int, sub string) bool {
	return i+len(sub) <= len(s) && s[i:i+len(sub)] == sub
}

// --- sectioning ---------------------------------------------------------

type qaSection struct {
	Chapter   string
	Question  string
	Body      string
	BodyStart int // rune 偏移（剥完 frontmatter/HTML 之后的文本内）
}

// extractQASections 按行扫描，识别 ##（chapter，仅作 context）/ ###（Q&A 边界）。
// code fence 内部的行不参与标题识别，避免 python 注释 "### foo" 误伤。
func extractQASections(text string) []qaSection {
	// 预算每行行首的 rune 偏移
	lines := strings.Split(text, "\n")
	lineStart := make([]int, len(lines)+1)
	off := 0
	for i, l := range lines {
		lineStart[i] = off
		off += utf8.RuneCountInString(l) + 1 // \n
	}
	lineStart[len(lines)] = off

	var sections []qaSection
	var chapter, question string
	var bodyLines []string
	bodyStart := 0
	inFence := false

	flush := func() {
		if question == "" {
			return
		}
		body := strings.TrimSpace(strings.Join(bodyLines, "\n"))
		if body != "" {
			sections = append(sections, qaSection{
				Chapter:   chapter,
				Question:  question,
				Body:      body,
				BodyStart: bodyStart,
			})
		}
		question = ""
		bodyLines = nil
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
		}
		if !inFence {
			if strings.HasPrefix(line, "### ") {
				flush()
				question = cleanHeading(line[4:])
				if i+1 < len(lines) {
					bodyStart = lineStart[i+1]
				} else {
					bodyStart = lineStart[len(lines)]
				}
				continue
			}
			if strings.HasPrefix(line, "## ") {
				flush()
				chapter = cleanHeading(line[3:])
				continue
			}
		}
		if question != "" {
			bodyLines = append(bodyLines, line)
		}
	}
	flush()
	return sections
}

// cleanHeading 去掉末尾 "#" 装饰、包裹的 ** 加粗。
func cleanHeading(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "# ")
	s = strings.TrimSpace(s)
	for len(s) >= 4 && strings.HasPrefix(s, "**") && strings.HasSuffix(s, "**") {
		s = strings.TrimSpace(s[2 : len(s)-2])
	}
	return s
}

// --- prefix rendering ---------------------------------------------------

func renderPrefix(sec qaSection, fragment bool) string {
	label := "答案："
	if fragment {
		label = "答案片段："
	}
	var b strings.Builder
	if sec.Chapter != "" {
		b.WriteString("章节：")
		b.WriteString(sec.Chapter)
		b.WriteByte('\n')
	}
	b.WriteString("问题：")
	b.WriteString(sec.Question)
	b.WriteString("\n\n")
	b.WriteString(label)
	b.WriteByte('\n')
	return b.String()
}
