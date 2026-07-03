package tools

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
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
//
// What we intentionally skip in v1: numbering / bullet markers, footnotes,
// comments, headers/footers, embedded images. Those pieces can be added
// later if resumes / reports need them.
func extractDOCX(abs string) (*ExtractDocumentTextOutput, error) {
	zr, err := zip.OpenReader(abs)
	if err != nil {
		return nil, fmt.Errorf("open docx: %w", err)
	}
	defer zr.Close()

	var docXML *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docXML = f
			break
		}
	}
	if docXML == nil {
		return nil, fmt.Errorf("not a valid docx: missing word/document.xml")
	}

	rc, err := docXML.Open()
	if err != nil {
		return nil, fmt.Errorf("open document.xml: %w", err)
	}
	defer rc.Close()

	text, warnings, err := parseDocxXML(rc)
	if err != nil {
		return nil, fmt.Errorf("parse document.xml: %w", err)
	}

	truncated := false
	if len(text) > maxExtractBytes {
		text = text[:maxExtractBytes]
		truncated = true
		warnings = append(warnings, fmt.Sprintf(
			"content truncated at %d KiB", maxExtractBytes/1024))
	}

	if strings.TrimSpace(text) == "" {
		warnings = append(warnings,
			"no text extracted — the document may be image-only or use non-standard formatting; OCR is not supported")
	}

	return &ExtractDocumentTextOutput{
		Path:      abs,
		Format:    "docx",
		Content:   text,
		Truncated: truncated,
		Warnings:  warnings,
	}, nil
}

// parseDocxXML streams the WordProcessingML document, buffering one paragraph
// at a time and flushing at </w:p>. Table cells are joined with ` | ` and
// wrapped with `|` at the row edges.
func parseDocxXML(r io.Reader) (string, []string, error) {
	var out strings.Builder
	var warnings []string

	// Per-paragraph mutable state.
	var para strings.Builder
	headingLevel := 0 // 0 = normal paragraph, 1-6 = heading level

	// Table cell state: cell text accumulates into `para`, but we want to
	// join cells with " | " and rows with "\n". We track whether we're
	// inside a table so we can format on </w:tc> and </w:tr>.
	inTable := 0    // depth of nested tables (usually 0 or 1)
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
			// Paragraph inside a table cell: keep text; cell flush handles
			// joining. But if the para has content, keep it as-is (may
			// have newlines within-cell — rare).
			return
		}
		trimmed := strings.TrimRight(text, " \t")
		if headingLevel > 0 {
			prefix := strings.Repeat("#", headingLevel) + " "
			out.WriteString(prefix)
		}
		out.WriteString(trimmed)
		out.WriteString("\n\n")
		headingLevel = 0
	}

	dec := xml.NewDecoder(r)
	inT := false // inside <w:t>
	skipDepth := 0 // >0 while inside a subtree we want to drop (e.g. w:instrText)

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
			name := t.Name.Local
			switch name {
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
			case "tr":
				// nothing; cells accumulate as we go
			case "tc":
				// nothing; text collects into para
			case "instrText", "fldChar":
				// Field instructions like PAGE / TOC / HYPERLINK — user
				// text is elsewhere in w:t; drop the raw instruction body.
				skipDepth = 1
			}
		case xml.EndElement:
			if skipDepth > 0 {
				skipDepth--
				continue
			}
			name := t.Name.Local
			switch name {
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
// Word style ids look like "Heading1", "Heading2", … "heading 1" etc. across
// versions and localisations; the numeric suffix is the reliable signal.
func parseHeadingLevel(styleVal string) int {
	v := strings.ToLower(styleVal)
	if !strings.HasPrefix(v, "heading") {
		return 0
	}
	// find first digit
	for i := len("heading"); i < len(v); i++ {
		c := v[i]
		if c >= '1' && c <= '6' {
			return int(c - '0')
		}
	}
	return 0
}
