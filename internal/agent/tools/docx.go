package tools

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// extractDOCX pulls plain text out of a .docx file by unzipping and walking
// `word/document.xml`. We handle enough of OOXML to make everyday resumes and
// reports readable:
//
//   - w:t         → text content
//   - w:br        → line break inside a paragraph
//   - w:tab       → tab character
//   - w:p         → paragraph boundary (double newline in output)
//   - w:pStyle    → header (Heading1..Heading6 map to '#'..'######')
//   - w:tbl/tr/tc → tables flattened to `| a | b |` lines
//   - a:blip      → embedded image; when tesseract is available we OCR its
//                    bytes and emit an `[embedded image OCR: name]` block
//                    inline at the image's XML position. Failures fold into
//                    a single aggregated warning per reason.
//
// What we intentionally skip: numbering / bullet markers, footnotes,
// comments, headers/footers, SmartArt / charts, VML legacy images.
func extractDOCX(ctx context.Context, abs string) (*ExtractDocumentTextOutput, error) {
	zr, err := zip.OpenReader(abs)
	if err != nil {
		return nil, fmt.Errorf("open docx: %w", err)
	}
	defer zr.Close()

	var docXML, relsXML *zip.File
	media := make(map[string]*zip.File)
	for _, f := range zr.File {
		switch {
		case f.Name == "word/document.xml":
			docXML = f
		case f.Name == "word/_rels/document.xml.rels":
			relsXML = f
		case strings.HasPrefix(f.Name, "word/media/"):
			media[f.Name] = f
		}
	}
	if docXML == nil {
		return nil, fmt.Errorf("not a valid docx: missing word/document.xml")
	}

	// relMap is nil-safe: parseDocxXML tolerates a nil map (no images
	// resolve, and the a:blip handling just no-ops).
	var relMap map[string]*zip.File
	if relsXML != nil {
		relMap = parseOOXMLRels(relsXML, media, "word/")
	}

	rc, err := docXML.Open()
	if err != nil {
		return nil, fmt.Errorf("open document.xml: %w", err)
	}
	defer rc.Close()

	collector := newOCRCollector(ctx, docOCRBudget)
	text, warnings, err := parseDocxXML(rc, relMap, collector)
	if err != nil {
		return nil, fmt.Errorf("parse document.xml: %w", err)
	}
	warnings = append(warnings, collector.warnings()...)

	truncated := false
	if len(text) > maxExtractBytes {
		text = text[:maxExtractBytes]
		truncated = true
		warnings = append(warnings, fmt.Sprintf(
			"content truncated at %d KiB", maxExtractBytes/1024))
	}

	if strings.TrimSpace(text) == "" {
		warnings = append(warnings,
			"no text extracted — the document may be image-only or use non-standard formatting")
	}

	return &ExtractDocumentTextOutput{
		Path:      abs,
		Format:    "docx",
		Content:   text,
		Truncated: truncated,
		Warnings:  warnings,
	}, nil
}

// parseOOXMLRels reads a `.rels` file and returns a map from Relationship Id
// to the media zip.File it targets. Shared by DOCX and PPTX. Only image
// relationships are included; hyperlinks / styles / fonts are skipped.
// `partDir` is the directory of the part the rels file belongs to (e.g.
// "word/" for word/_rels/document.xml.rels, "ppt/slides/" for
// ppt/slides/_rels/slide1.xml.rels), used to resolve relative targets.
func parseOOXMLRels(f *zip.File, media map[string]*zip.File, partDir string) map[string]*zip.File {
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()

	type rel struct {
		ID     string `xml:"Id,attr"`
		Type   string `xml:"Type,attr"`
		Target string `xml:"Target,attr"`
	}
	var doc struct {
		Rels []rel `xml:"Relationship"`
	}
	if err := xml.NewDecoder(rc).Decode(&doc); err != nil {
		return nil
	}

	out := make(map[string]*zip.File, len(doc.Rels))
	for _, r := range doc.Rels {
		if !strings.Contains(r.Type, "image") &&
			!strings.HasPrefix(r.Target, "media/") &&
			!strings.HasPrefix(r.Target, "../media/") {
			continue
		}
		target := resolveRelTarget(r.Target, partDir)
		if zf, ok := media[target]; ok {
			out[r.ID] = zf
		}
	}
	return out
}

// resolveRelTarget resolves a Relationship Target against the part's
// directory. Handles absolute ("/word/media/x.png"), parent-relative
// ("../media/x.png"), and sibling-relative ("media/x.png") targets.
func resolveRelTarget(target, partDir string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(target, "/")
	}
	return filepath.ToSlash(filepath.Clean(partDir + target))
}

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// parseDocxXML streams the WordProcessingML document, buffering one
// paragraph at a time and flushing at </w:p>. Table cells are joined with
// ` | ` and wrapped with `|` at the row edges. Embedded images are
// dispatched to the collector; successful OCR text is emitted inline at
// the image's position.
func parseDocxXML(r io.Reader, relMap map[string]*zip.File, oc *ocrCollector) (string, []string, error) {
	var out strings.Builder
	var warnings []string

	var para strings.Builder
	headingLevel := 0

	inTable := 0
	rowCells := []string{}
	tableRows := []string{}

	flushCellText := func() {
		rowCells = append(rowCells, strings.TrimSpace(para.String()))
		para.Reset()
		headingLevel = 0
	}
	flushRow := func() {
		if len(rowCells) == 0 {
			return
		}
		tableRows = append(tableRows, "| "+strings.Join(rowCells, " | ")+" |")
		rowCells = rowCells[:0]
	}
	flushTable := func() {
		if len(tableRows) == 0 {
			return
		}
		for _, r := range tableRows {
			out.WriteString(r)
			out.WriteByte('\n')
		}
		out.WriteByte('\n')
		tableRows = tableRows[:0]
	}
	flushPara := func() {
		text := para.String()
		para.Reset()
		if inTable > 0 {
			return
		}
		trimmed := strings.TrimRight(text, " \t")
		if headingLevel > 0 {
			out.WriteString(strings.Repeat("#", headingLevel) + " ")
		}
		out.WriteString(trimmed)
		out.WriteString("\n\n")
		headingLevel = 0
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
			return // soft/hard fail — nothing goes in the body
		}

		if inTable > 0 {
			// Cells are one visual line in `| a | b |`; collapse whitespace
			// so OCR newlines don't break the row.
			if para.Len() > 0 && !strings.HasSuffix(para.String(), " ") {
				para.WriteByte(' ')
			}
			para.WriteString("[embedded image OCR: ")
			para.WriteString(name)
			para.WriteString("] ")
			para.WriteString(collapseWhitespace(text))
			return
		}

		// Outside a table: give the image its own block. Flush any
		// preceding text as its own paragraph so the "before" context
		// stays with the image in reading order.
		if strings.TrimSpace(para.String()) != "" {
			flushPara()
		} else {
			para.Reset()
			headingLevel = 0
		}
		out.WriteString("[embedded image OCR: ")
		out.WriteString(name)
		out.WriteString("]\n")
		out.WriteString(text)
		out.WriteString("\n\n")
	}

	dec := xml.NewDecoder(r)
	inT := false
	skipDepth := 0

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
			if skipDepth > 0 {
				skipDepth++
				continue
			}
			switch t.Name.Local {
			case "t":
				inT = true
			case "br":
				if inTable == 0 {
					para.WriteByte('\n')
				} else {
					para.WriteByte(' ')
				}
			case "tab":
				para.WriteByte('\t')
			case "pStyle":
				for _, a := range t.Attr {
					if a.Name.Local == "val" {
						headingLevel = parseHeadingLevel(a.Value)
						break
					}
				}
			case "tbl":
				inTable++
			case "blip":
				// DrawingML image reference. r:embed carries the rels Id;
				// r:link (external images) is not supported in v1.
				for _, a := range t.Attr {
					if a.Name.Local == "embed" && a.Value != "" {
						emitImage(a.Value)
						break
					}
				}
			case "instrText", "fldChar":
				skipDepth = 1
			}
		case xml.EndElement:
			if skipDepth > 0 {
				skipDepth--
				continue
			}
			switch t.Name.Local {
			case "t":
				inT = false
			case "p":
				flushPara()
			case "tc":
				flushCellText()
			case "tr":
				flushRow()
			case "tbl":
				flushTable()
				inTable--
				if inTable < 0 {
					inTable = 0
				}
			}
		case xml.CharData:
			if skipDepth == 0 && inT {
				para.Write(t)
			}
		}
	}
	flushPara()
	flushTable()

	return strings.TrimRight(out.String(), "\n"), warnings, nil
}

// parseHeadingLevel maps a w:pStyle w:val to a heading level 1..6.
// Word style ids look like "Heading1", "Heading2", … "heading 1" etc.
// across versions and localisations; the numeric suffix is the reliable signal.
func parseHeadingLevel(styleVal string) int {
	v := strings.ToLower(styleVal)
	if !strings.HasPrefix(v, "heading") {
		return 0
	}
	for i := len("heading"); i < len(v); i++ {
		c := v[i]
		if c >= '1' && c <= '6' {
			return int(c - '0')
		}
	}
	return 0
}
