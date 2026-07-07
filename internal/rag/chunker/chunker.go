// Package chunker 提供递归字符切分（recursive character splitter）。
//
// 算法：优先级从高到低尝试分隔符（段落 → 换行 → 中英句号 → ...），
// 找到当前层级的切点后把每段递归下切，任何一段 <= Size 就保留，
// 否则用下一级分隔符继续切。分隔符本身归到左侧片段（保证无丢字）。
// 最后按 Size 贪心打包，chunk 之间加 Overlap（rune 数）。
//
// 全流程在 []rune 空间做，CharStart/CharEnd 是原文的 rune 下标。
package chunker

type Chunk struct {
	Ord       int
	Content   string
	CharStart int // 原文 rune 起点，含
	CharEnd   int // 原文 rune 终点，不含
}

// Chunker 是切分器统一接口。当前实现：
//   - Splitter：通用递归字符切分（本文件）
//   - MarkdownSplitter：Q&A markdown 语义切分（markdown.go）
type Chunker interface {
	Split(text string) []Chunk
}

// DefaultSeps 中英混排通用分隔符表；末尾 "" 是硬切 fallback。
var DefaultSeps = []string{
	"\n\n", "\n",
	"。", "！", "？", "；",
	". ", "! ", "? ", "; ",
	"，", ", ",
	" ",
	"",
}

type Splitter struct {
	Size    int
	Overlap int
	Seps    []string
}

// New 构造 Splitter。size<=0 用 500，overlap<0 归零，overlap>=size 退为 size/4。
func New(size, overlap int) *Splitter {
	if size <= 0 {
		size = 500
	}
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= size {
		overlap = size / 4
	}
	return &Splitter{Size: size, Overlap: overlap, Seps: DefaultSeps}
}

func (s *Splitter) Split(text string) []Chunk {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	atoms := s.atomize(runes, 0, s.Seps)
	return s.pack(runes, atoms)
}

// atomize 返回一组 [start,end) rune 区间，完整平铺 runes，每段 <= Size。
// base 是本次调用对应片段在原文里的 rune 起点。
func (s *Splitter) atomize(runes []rune, base int, seps []string) [][2]int {
	if len(runes) <= s.Size {
		return [][2]int{{base, base + len(runes)}}
	}
	for i, sep := range seps {
		if sep == "" {
			// 所有分隔符都不行 → 按 Size 硬切
			var out [][2]int
			for j := 0; j < len(runes); j += s.Size {
				end := j + s.Size
				if end > len(runes) {
					end = len(runes)
				}
				out = append(out, [2]int{base + j, base + end})
			}
			return out
		}
		sepR := []rune(sep)
		cuts := findAll(runes, sepR)
		if len(cuts) == 0 {
			continue
		}
		// 按 cuts 切；分隔符归左侧片段
		var pieces [][2]int
		prev := 0
		for _, c := range cuts {
			pieces = append(pieces, [2]int{prev, c + len(sepR)})
			prev = c + len(sepR)
		}
		if prev < len(runes) {
			pieces = append(pieces, [2]int{prev, len(runes)})
		}
		var out [][2]int
		for _, p := range pieces {
			if p[1]-p[0] == 0 {
				continue
			}
			if p[1]-p[0] <= s.Size {
				out = append(out, [2]int{base + p[0], base + p[1]})
			} else {
				// 本层分隔符切完仍太大，继续用更细粒度分隔符
				out = append(out, s.atomize(runes[p[0]:p[1]], base+p[0], seps[i+1:])...)
			}
		}
		return out
	}
	// seps 末尾必是 ""，正常不会走到这
	return [][2]int{{base, base + len(runes)}}
}

// pack 把 atoms 贪心打包为 chunk（每个 <= Size），再加 overlap。
func (s *Splitter) pack(runes []rune, atoms [][2]int) []Chunk {
	if len(atoms) == 0 {
		return nil
	}
	var chunks [][2]int
	curStart := atoms[0][0]
	curEnd := atoms[0][1]
	for i := 1; i < len(atoms); i++ {
		a := atoms[i]
		if a[1]-curStart <= s.Size {
			curEnd = a[1]
		} else {
			chunks = append(chunks, [2]int{curStart, curEnd})
			curStart = a[0]
			curEnd = a[1]
		}
	}
	chunks = append(chunks, [2]int{curStart, curEnd})

	// overlap：后续 chunk 起点前推 Overlap 个 rune，但不越过上一 chunk 的起点
	if s.Overlap > 0 {
		for i := 1; i < len(chunks); i++ {
			ns := chunks[i][0] - s.Overlap
			if ns < 0 {
				ns = 0
			}
			if ns < chunks[i-1][0] {
				ns = chunks[i-1][0]
			}
			chunks[i][0] = ns
		}
	}

	out := make([]Chunk, len(chunks))
	for i, c := range chunks {
		out[i] = Chunk{
			Ord:       i,
			Content:   string(runes[c[0]:c[1]]),
			CharStart: c[0],
			CharEnd:   c[1],
		}
	}
	return out
}

// findAll 返回 needle 在 hay 中所有非重叠出现位置（rune 下标）。
func findAll(hay, needle []rune) []int {
	if len(needle) == 0 || len(needle) > len(hay) {
		return nil
	}
	var out []int
	for i := 0; i+len(needle) <= len(hay); {
		match := true
		for j, r := range needle {
			if hay[i+j] != r {
				match = false
				break
			}
		}
		if match {
			out = append(out, i)
			i += len(needle)
		} else {
			i++
		}
	}
	return out
}
