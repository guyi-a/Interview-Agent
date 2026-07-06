package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/ledongthuc/pdf"

	"github.com/guyi-a/Interview-Agent/internal/agent/scope"
	"github.com/guyi-a/Interview-Agent/internal/ocr"
)

const (
	// Same cap as read_file — keep context budgets predictable.
	maxExtractBytes = 256 * 1024
)

type ExtractDocumentTextInput struct {
	Path     string `json:"path" jsonschema:"description=File path (absolute local path or workspace-relative)."`
	PageFrom int    `json:"page_from,omitempty" jsonschema:"description=First page to extract (1-based). Omit or 0 to start from page 1. Only used for PDF."`
	PageTo   int    `json:"page_to,omitempty" jsonschema:"description=Last page to extract (inclusive). Omit or 0 to extract to the last page. Only used for PDF."`
}

type ExtractDocumentTextMetadata struct {
	Pages    int `json:"pages,omitempty"`
	PageFrom int `json:"page_from,omitempty"`
	PageTo   int `json:"page_to,omitempty"`
}

type ExtractDocumentTextOutput struct {
	Path      string                      `json:"path"`
	Format    string                      `json:"format"`
	Content   string                      `json:"content"`
	Truncated bool                        `json:"truncated,omitempty"`
	Metadata  ExtractDocumentTextMetadata `json:"metadata,omitempty"`
	Warnings  []string                    `json:"warnings,omitempty"`
}

func newExtractDocumentTextTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *ExtractDocumentTextInput) (*ExtractDocumentTextOutput, error) {
		if in.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		ws, wsErr := d.resolveWorkspace(ctx)
		if wsErr != nil && !filepath.IsAbs(in.Path) {
			return nil, wsErr
		}
		abs, err := scope.ResolveRead(ws, in.Path)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("stat: %w", err)
		}
		if st.IsDir() {
			return nil, fmt.Errorf("%q is a directory", in.Path)
		}

		ext := strings.ToLower(filepath.Ext(abs))
		switch ext {
		case ".pdf":
			return extractPDF(abs, in.PageFrom, in.PageTo)
		case ".docx":
			return extractDOCX(ctx, abs)
		case ".pptx":
			return extractPPTX(ctx, abs, in.PageFrom, in.PageTo)
		case ".png", ".jpg", ".jpeg", ".webp", ".bmp", ".tif", ".tiff":
			return extractImage(ctx, abs)
		case ".xlsx", ".ipynb":
			return nil, fmt.Errorf(
				"format %s is not yet supported by extract_document_text; supported: .pdf, .docx, .pptx, images (.png/.jpg/.jpeg/.webp/.bmp/.tiff)",
				ext,
			)
		default:
			return nil, fmt.Errorf(
				"unsupported file type %q for extract_document_text; use read_file for text files or file_info to check kind",
				ext,
			)
		}
	}
	return utils.InferTool(
		"extract_document_text",
		"Extract plain text from a binary document or image. Supported: .pdf, .docx, .pptx, images (.png/.jpg/.jpeg/.webp/.bmp/.tiff). Not supported yet: .xlsx, .ipynb. "+
			"PDF output has '--- Page N ---' markers; PPTX has '--- Slide N ---'; page_from / page_to (1-based, inclusive) limit the range for large PDFs / decks (ignored for images). "+
			"DOCX output preserves paragraph breaks (blank line between), maps Heading1..Heading6 styles to '#'..'######', and flattens tables to '| a | b |' rows. "+
			"PPTX output covers visible slide text; speaker notes and charts are dropped. "+
			"DOCX/PPTX embedded images (screenshots, diagrams, etc.) are inline-OCR'd when tesseract is installed on the host: text appears as '[embedded image OCR: name.png]\\n{recognized text}' at the image's position in the document. "+
			"Standalone image files (.png/.jpg/etc.) are OCR'd directly — the returned content is the recognized text (may be empty if the image has no readable text). "+
			"OCR is best-effort: text can be noisy or wrong. Tell the user any content coming from OCR was 'read from an image' rather than quoted as authoritative. "+
			"If tesseract is missing or an image can't be OCR'd, no marker is inserted; a deduplicated summary lands in warnings (e.g. '3 embedded images skipped: tesseract not installed'). "+
			"Scanned PDFs (whole-page images) are still unsupported — the returned content will be empty and a warning will say so; ask the user for a .docx source or an already-OCR'd PDF. "+
			"Content is truncated at 256 KiB.",
		fn,
	)
}

// extractImage handles standalone image files by running tesseract on the
// raw file bytes. Uses a longer per-image timeout than the embedded-image
// path because standalone screenshots tend to be higher-resolution than
// what gets embedded in a DOCX / PPTX.
func extractImage(ctx context.Context, abs string) (*ExtractDocumentTextOutput, error) {
	out := &ExtractDocumentTextOutput{
		Path:   abs,
		Format: "image",
	}
	if !ocr.Available() {
		out.Warnings = append(out.Warnings,
			ocr.WarnNotInstalled+"; install tesseract on the host to enable image OCR (macOS: brew install tesseract tesseract-lang)")
		return out, nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	res, err := ocr.RunWithOptions(ctx, data, filepath.Base(abs), &ocr.Options{
		Timeout: 30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("ocr: %w", err)
	}
	if res.Warning != "" {
		out.Warnings = append(out.Warnings, res.Warning)
	}
	if len(res.Text) > maxExtractBytes {
		out.Content = res.Text[:maxExtractBytes]
		out.Truncated = true
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("content truncated at %d KiB", maxExtractBytes/1024))
	} else {
		out.Content = res.Text
	}
	return out, nil
}

func extractPDF(abs string, pageFrom, pageTo int) (*ExtractDocumentTextOutput, error) {
	f, r, err := pdf.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("open pdf: %w", err)
	}
	defer f.Close()

	total := r.NumPage()
	if total <= 0 {
		return nil, fmt.Errorf("pdf has no pages")
	}

	from, to := clampPageRange(pageFrom, pageTo, total)

	var buf strings.Builder
	var warnings []string
	truncated := false
	textPagesFound := 0

	for i := from; i <= to; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("page %d: %v", i, err))
			continue
		}
		if strings.TrimSpace(text) != "" {
			textPagesFound++
		}
		header := fmt.Sprintf("--- Page %d ---\n", i)
		if buf.Len()+len(header)+len(text)+1 > maxExtractBytes {
			// Try to fit the header at least, and truncate the text.
			remaining := maxExtractBytes - buf.Len() - len(header) - 1
			if remaining > 0 {
				buf.WriteString(header)
				if remaining >= len(text) {
					buf.WriteString(text)
					buf.WriteByte('\n')
				} else {
					buf.WriteString(text[:remaining])
					buf.WriteByte('\n')
				}
			}
			truncated = true
			break
		}
		buf.WriteString(header)
		buf.WriteString(text)
		buf.WriteByte('\n')
	}

	if textPagesFound == 0 {
		warnings = append(warnings,
			"no text extracted from any page — this PDF is likely a scanned image; OCR is not supported")
	}

	if truncated {
		warnings = append(warnings, fmt.Sprintf(
			"content truncated at %d KiB — use page_from / page_to to fetch further pages", maxExtractBytes/1024))
	}

	return &ExtractDocumentTextOutput{
		Path:      abs,
		Format:    "pdf",
		Content:   buf.String(),
		Truncated: truncated,
		Metadata: ExtractDocumentTextMetadata{
			Pages:    total,
			PageFrom: from,
			PageTo:   to,
		},
		Warnings: warnings,
	}, nil
}

// clampPageRange normalizes a user-supplied [from, to] range against a
// document of totalPages. Zeros mean "unbounded end".
func clampPageRange(from, to, total int) (int, int) {
	if from <= 0 {
		from = 1
	}
	if to <= 0 || to > total {
		to = total
	}
	if from > total {
		from = total
	}
	if to < from {
		to = from
	}
	return from, to
}
