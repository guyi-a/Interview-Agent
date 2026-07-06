package tools

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/guyi-a/Interview-Agent/internal/ocr"
)

// docOCRBudget caps total tesseract wall time per document. At ~0.5-1s per
// image on a modern laptop, 30s tolerates ~30 images before the tail starts
// getting skipped. Overflow becomes a single deduped warning.
const docOCRBudget = 30 * time.Second

// ocrCollector orchestrates per-image OCR calls with a shared document-wide
// time budget, and aggregates soft failures into deduplicated warnings so a
// 20-image doc missing tesseract yields one warning, not twenty.
//
// Not thread-safe — one collector per extraction call.
type ocrCollector struct {
	ctx       context.Context
	docBudget time.Duration
	docUsed   time.Duration
	counts    map[string]int
	total     int
}

func newOCRCollector(ctx context.Context, budget time.Duration) *ocrCollector {
	if budget <= 0 {
		budget = docOCRBudget
	}
	return &ocrCollector{
		ctx:       ctx,
		docBudget: budget,
		counts:    make(map[string]int),
	}
}

// tryOCR runs OCR on one image. Returns (cleaned text, true) on success, or
// ("", false) if the caller should skip emitting anything for this image.
// Soft failures are aggregated into the collector for later reporting; hard
// errors are also aggregated (never propagated) so a single bad image can't
// abort the whole extraction.
func (c *ocrCollector) tryOCR(data []byte, name string) (string, bool) {
	c.total++
	if c.docUsed >= c.docBudget {
		c.counts["OCR skipped: doc time budget exceeded"]++
		return "", false
	}
	start := time.Now()
	res, err := ocr.Run(c.ctx, data, name)
	c.docUsed += time.Since(start)
	if err != nil {
		c.counts[fmt.Sprintf("OCR error: %v", err)]++
		return "", false
	}
	if res.Text == "" {
		reason := res.Warning
		if reason == "" {
			reason = ocr.WarnNoText
		}
		c.counts[reason]++
		return "", false
	}
	return cleanOCRText(res.Text), true
}

// warnings summarises collected soft failures with a stable sorted order.
func (c *ocrCollector) warnings() []string {
	if len(c.counts) == 0 {
		return nil
	}
	reasons := make([]string, 0, len(c.counts))
	for r := range c.counts {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	out := make([]string, 0, len(reasons))
	for _, r := range reasons {
		n := c.counts[r]
		noun := "embedded images"
		if n == 1 {
			noun = "embedded image"
		}
		out = append(out, fmt.Sprintf("%d %s skipped: %s", n, noun, r))
	}
	return out
}

var multiNewline = regexp.MustCompile(`\n{3,}`)

// cleanOCRText normalises tesseract output: trim outer whitespace, right-trim
// each line, and collapse runs of 3+ blank lines. Deliberately conservative —
// internal spacing is preserved so column-aligned layouts survive.
func cleanOCRText(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t")
	}
	return multiNewline.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
}

// collapseWhitespace flattens all internal whitespace runs (including
// newlines) to a single space — used when we have to keep OCR text on a
// single line, e.g. inside a table cell.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
