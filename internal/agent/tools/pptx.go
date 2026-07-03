package tools

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// extractPPTX pulls visible slide text out of a .pptx by unzipping and
// walking every `ppt/slides/slide{N}.xml`. Each slide's paragraphs are
// separated by single newlines and each slide is fronted with
// `--- Slide N ---` so downstream tools (or the agent itself) can tell
// slides apart.
//
// slideFrom / slideTo (1-based, inclusive; 0 = unbounded) let callers page
// through big decks without blowing the 256 KiB output cap.
//
// v1 covers: text runs (a:t), paragraph breaks (a:p), soft breaks (a:br).
// v1 skips: notes (ppt/notesSlides), speaker comments, images, charts,
// table borders. Notes / tables can be added later once we see real files
// that need them.
func extractPPTX(abs string, slideFrom, slideTo int) (*ExtractDocumentTextOutput, error) {
	zr, err := zip.OpenReader(abs)
	if err != nil {
		return nil, fmt.Errorf("open pptx: %w", err)
	}
	defer zr.Close()

	slides, err := collectSlides(zr)
	if err != nil {
		return nil, err
	}
	if len(slides) == 0 {
		return nil, fmt.Errorf("not a valid pptx: no slides found")
	}

	total := len(slides)
	from, to := clampPageRange(slideFrom, slideTo, total)

	var buf strings.Builder
	var warnings []string
	truncated := false
	nonEmptyCount := 0

	for _, s := range slides {
		if s.num < from || s.num > to {
			continue
		}
		rc, err := s.f.Open()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("slide %d: open: %v", s.num, err))
			continue
		}
		text, err := parsePPTXSlide(rc)
		rc.Close()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("slide %d: parse: %v", s.num, err))
			continue
		}
		if strings.TrimSpace(text) != "" {
			nonEmptyCount++
		}
		header := fmt.Sprintf("--- Slide %d ---\n", s.num)
		if buf.Len()+len(header)+len(text)+2 > maxExtractBytes {
			// Fit the header + as much text as we can, then bail.
			remaining := maxExtractBytes - buf.Len() - len(header) - 2
			if remaining > 0 {
				buf.WriteString(header)
				if remaining >= len(text) {
					buf.WriteString(text)
				} else {
					buf.WriteString(text[:remaining])
				}
				buf.WriteString("\n\n")
			}
			truncated = true
			break
		}
		buf.WriteString(header)
		buf.WriteString(text)
		buf.WriteString("\n\n")
	}

	if nonEmptyCount == 0 {
		warnings = append(warnings,
			"no text extracted from any slide — this PPTX may be image-only; OCR is not supported")
	}
	if truncated {
		warnings = append(warnings, fmt.Sprintf(
			"content truncated at %d KiB — use page_from / page_to to fetch further slides",
			maxExtractBytes/1024))
	}

	return &ExtractDocumentTextOutput{
		Path:      abs,
		Format:    "pptx",
		Content:   strings.TrimRight(buf.String(), "\n"),
		Truncated: truncated,
		Metadata: ExtractDocumentTextMetadata{
			Pages:    total,
			PageFrom: from,
			PageTo:   to,
		},
		Warnings: warnings,
	}, nil
}

type pptxSlide struct {
	num int
	f   *zip.File
}

var pptxSlideRE = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)

func collectSlides(zr *zip.ReadCloser) ([]pptxSlide, error) {
	var slides []pptxSlide
	for _, f := range zr.File {
		m := pptxSlideRE.FindStringSubmatch(f.Name)
		if m == nil {
			continue
		}
		num, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		slides = append(slides, pptxSlide{num: num, f: f})
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].num < slides[j].num })
	return slides, nil
}

// parsePPTXSlide walks a single slide's XML. DrawingML uses the `a:`
// namespace: `<a:t>` for text runs, `<a:p>` for paragraphs, `<a:br/>` for
// soft breaks inside a paragraph.
func parsePPTXSlide(r io.Reader) (string, error) {
	var out strings.Builder
	var para strings.Builder

	flushPara := func() {
		text := strings.TrimRight(para.String(), " \t")
		para.Reset()
		if text == "" {
			return
		}
		out.WriteString(text)
		out.WriteByte('\n')
	}

	dec := xml.NewDecoder(r)
	inT := false

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inT = true
			case "br":
				para.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "p":
				flushPara()
			}
		case xml.CharData:
			if inT {
				para.Write(t)
			}
		}
	}
	flushPara()

	return strings.TrimRight(out.String(), "\n"), nil
}
