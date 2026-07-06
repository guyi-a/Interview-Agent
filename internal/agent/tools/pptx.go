package tools

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
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
// v1 covers: text runs (a:t), paragraph breaks (a:p), soft breaks (a:br),
// embedded images (a:blip) → OCR'd inline at their XML position via the
// shared collector. Each slide has its own rels file mapping rId → image
// in ppt/media/.
//
// v1 skips: notes (ppt/notesSlides), speaker comments, charts, table borders.
func extractPPTX(ctx context.Context, abs string, slideFrom, slideTo int) (*ExtractDocumentTextOutput, error) {
	zr, err := zip.OpenReader(abs)
	if err != nil {
		return nil, fmt.Errorf("open pptx: %w", err)
	}
	defer zr.Close()

	media := make(map[string]*zip.File)
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "ppt/media/") {
			media[f.Name] = f
		}
	}

	slides, err := collectSlides(zr)
	if err != nil {
		return nil, err
	}
	if len(slides) == 0 {
		return nil, fmt.Errorf("not a valid pptx: no slides found")
	}

	total := len(slides)
	from, to := clampPageRange(slideFrom, slideTo, total)

	collector := newOCRCollector(ctx, docOCRBudget)

	var buf strings.Builder
	var warnings []string
	truncated := false
	nonEmptyCount := 0

	for _, s := range slides {
		if s.num < from || s.num > to {
			continue
		}

		// Each slide has its own rels — load lazily.
		var relMap map[string]*zip.File
		if s.rels != nil {
			relMap = parseOOXMLRels(s.rels, media, "ppt/slides/")
		}

		rc, err := s.f.Open()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("slide %d: open: %v", s.num, err))
			continue
		}
		text, slideWarnings, err := parsePPTXSlide(rc, relMap, collector)
		rc.Close()
		warnings = append(warnings, slideWarnings...)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("slide %d: parse: %v", s.num, err))
			continue
		}
		if strings.TrimSpace(text) != "" {
			nonEmptyCount++
		}
		header := fmt.Sprintf("--- Slide %d ---\n", s.num)
		if buf.Len()+len(header)+len(text)+2 > maxExtractBytes {
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

	warnings = append(warnings, collector.warnings()...)

	if nonEmptyCount == 0 {
		warnings = append(warnings,
			"no text extracted from any slide — this PPTX may be image-only")
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
	num  int
	f    *zip.File
	rels *zip.File // matching _rels/slideN.xml.rels, nil if the deck has no images
}

var (
	pptxSlideRE    = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)
	pptxSlideRelRE = regexp.MustCompile(`^ppt/slides/_rels/slide(\d+)\.xml\.rels$`)
)

func collectSlides(zr *zip.ReadCloser) ([]pptxSlide, error) {
	slidesByNum := map[int]*pptxSlide{}
	for _, f := range zr.File {
		if m := pptxSlideRE.FindStringSubmatch(f.Name); m != nil {
			num, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			s := slidesByNum[num]
			if s == nil {
				s = &pptxSlide{num: num}
				slidesByNum[num] = s
			}
			s.f = f
			continue
		}
		if m := pptxSlideRelRE.FindStringSubmatch(f.Name); m != nil {
			num, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			s := slidesByNum[num]
			if s == nil {
				s = &pptxSlide{num: num}
				slidesByNum[num] = s
			}
			s.rels = f
		}
	}
	slides := make([]pptxSlide, 0, len(slidesByNum))
	for _, s := range slidesByNum {
		if s.f == nil {
			continue // rels without a slide xml — malformed, skip
		}
		slides = append(slides, *s)
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].num < slides[j].num })
	return slides, nil
}

// parsePPTXSlide walks a single slide's XML. DrawingML uses the `a:`
// namespace: `<a:t>` for text runs, `<a:p>` for paragraphs, `<a:br/>` for
// soft breaks inside a paragraph, `<a:blip r:embed="X"/>` for image refs.
func parsePPTXSlide(r io.Reader, relMap map[string]*zip.File, oc *ocrCollector) (string, []string, error) {
	var out strings.Builder
	var para strings.Builder
	var warnings []string

	flushPara := func() {
		text := strings.TrimRight(para.String(), " \t")
		para.Reset()
		if text == "" {
			return
		}
		out.WriteString(text)
		out.WriteByte('\n')
	}

	emitImage := func(embedID string) {
		if relMap == nil {
			return
		}
		f, ok := relMap[embedID]
		if !ok {
			return
		}
		name := filepath.Base(f.Name)

		data, err := readZipEntry(f)
		if err != nil {
			warnings = append(warnings,
				fmt.Sprintf("read embedded image %s: %v", name, err))
			return
		}

		text, ok := oc.tryOCR(data, name)
		if !ok {
			return
		}

		// Slides don't have DOCX's table/cell distinction — always emit as
		// its own block. Flush any preceding text first so the "before"
		// context stays with the image in reading order.
		if strings.TrimSpace(para.String()) != "" {
			flushPara()
		} else {
			para.Reset()
		}
		out.WriteString("[embedded image OCR: ")
		out.WriteString(name)
		out.WriteString("]\n")
		out.WriteString(text)
		out.WriteString("\n")
	}

	dec := xml.NewDecoder(r)
	inT := false

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", warnings, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inT = true
			case "br":
				para.WriteByte('\n')
			case "blip":
				for _, a := range t.Attr {
					if a.Name.Local == "embed" && a.Value != "" {
						emitImage(a.Value)
						break
					}
				}
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

	return strings.TrimRight(out.String(), "\n"), warnings, nil
}
