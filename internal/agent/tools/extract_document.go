package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/ledongthuc/pdf"

	"github.com/guyi-a/Interview-Agent/internal/agent/scope"
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
			return extractDOCX(abs)
		case ".pptx":
			return extractPPTX(abs, in.PageFrom, in.PageTo)
		case ".xlsx", ".ipynb":
			return nil, fmt.Errorf(
				"format %s is not yet supported by extract_document_text; supported: .pdf, .docx, .pptx",
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
		"Extract plain text from a binary document. Supported: .pdf, .docx, .pptx. Not supported yet: .xlsx, .ipynb. "+
			"PDF output has '--- Page N ---' markers; PPTX has '--- Slide N ---'; page_from / page_to (1-based, inclusive) limit the range for large PDFs / decks. "+
			"DOCX output preserves paragraph breaks (blank line between), maps Heading1..Heading6 styles to '#'..'######', and flattens tables to '| a | b |' rows. "+
			"PPTX output covers visible slide text; speaker notes, embedded images, and charts are dropped. "+
			"Content is truncated at 256 KiB. "+
			"If the returned content is empty or a warning mentions 'no text', the document is likely image-only — OCR is not supported.",
		fn,
	)
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
