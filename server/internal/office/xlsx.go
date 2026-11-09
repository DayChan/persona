package office

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// A workbook from JSON or CSV. JSON: {"sheets":[{"name":"Sales","header":["Month","Revenue"],"rows":[["Jan",1200],["Total","=SUM(B2:B2)"]],
// "widths":[10,14],"formats":["","#,##0.00"],"freeze":true}]}. A cell is text, a number, true/false, or a formula
// ("=…"): formulas are worked out by Excel and Numbers when the file is opened (fullCalcOnLoad), so the totals stay
// formulas instead of numbers typed in. A header is bold, shaded and frozen; columns are as wide as what is in them
// unless widths say; formats are Excel number formats by column, and a date format turns "2026-11-04" into a date.

type workbookSpec struct {
	Sheets []sheetSpec `json:"sheets"`
}

type sheetSpec struct {
	Name    string    `json:"name"`
	Header  []string  `json:"header"`
	Rows    [][]any   `json:"rows"`
	Widths  []float64 `json:"widths"`
	Formats []string  `json:"formats"`
	Freeze  *bool     `json:"freeze"`
}

// Xlsx is the workbook JSON src describes.
func Xlsx(src []byte, title string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber()
	var spec workbookSpec
	if err := dec.Decode(&spec); err != nil {
		return nil, fmt.Errorf("the workbook's JSON: %w", err)
	}
	return writeXlsx(spec, title)
}

// XlsxFromCSV is a one-sheet workbook of CSV src, its first row the header. Integers stay
// numeric only when serialization keeps them exact, decimals when it changes no more than
// 15-digit rounding; IDs like 007 stay text.
func XlsxFromCSV(src []byte, name, title string) ([]byte, error) {
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))))
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("the CSV: %w", err)
	}
	if len(recs) == 0 {
		return nil, errEmpty
	}
	s := sheetSpec{Name: name, Header: recs[0]}
	for _, rec := range recs[1:] {
		row := make([]any, len(rec))
		for i, v := range rec {
			row[i] = v
			if f, err := strconv.ParseFloat(v, 64); err == nil && !leadingZero(v) && !math.IsInf(f, 0) && !math.IsNaN(f) && csvNumberExact(v, f) {
				row[i] = json.Number(v)
			}
		}
		s.Rows = append(s.Rows, row)
	}
	return writeXlsx(workbookSpec{Sheets: []sheetSpec{s}}, title)
}

func leadingZero(v string) bool {
	v = strings.TrimPrefix(strings.TrimPrefix(v, "-"), "+")
	return len(v) > 1 && v[0] == '0' && v[1] != '.'
}

func writeXlsx(spec workbookSpec, title string) ([]byte, error) {
	if len(spec.Sheets) == 0 {
		return nil, errors.New(`the workbook has no sheets: {"sheets":[{"name":…,"header":[…],"rows":[[…]]}]}`)
	}
	st := &xlsxStyles{fmts: map[string]int{}}
	book := &xbook{sheets: map[string]*xsheet{}}
	used := map[string]bool{}
	names := make([]string, len(spec.Sheets))
	grids := make([]*grid, len(spec.Sheets))
	for i, s := range spec.Sheets {
		names[i] = sheetName(s.Name, i, used)
		g, err := buildGrid(s, st)
		if err != nil {
			return nil, fmt.Errorf("sheet %q: %w", names[i], err)
		}
		grids[i] = g
		book.sheets[strings.ToLower(names[i])] = g.sheet
	}
	types := map[string]string{
		"xl/workbook.xml": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml",
		"xl/styles.xml":   "application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml",
	}
	var wbRels rels
	var sheets strings.Builder
	var parts []part
	for i, g := range grids {
		file := fmt.Sprintf("sheet%d.xml", i+1)
		types["xl/worksheets/"+file] = "application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"
		fmt.Fprintf(&sheets, `<sheet name="%s" sheetId="%d" r:id="%s"/>`, esc(names[i]), i+1, wbRels.add("worksheet", "worksheets/"+file, false))
		parts = append(parts, part{"xl/worksheets/" + file, g.xml(book, spec.Sheets[i])})
	}
	wbRels.add("styles", "styles.xml", false)
	workbook := xmlHead + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><bookViews><workbookView/></bookViews>` +
		`<sheets>` + sheets.String() + `</sheets><calcPr calcId="191029" fullCalcOnLoad="1"/></workbook>`
	return pack(append([]part{
		{"[Content_Types].xml", contentTypes(types)},
		{"_rels/.rels", rootRels(officeDocRel, "xl/workbook.xml")},
		{"docProps/core.xml", coreProps(title)},
		{"docProps/app.xml", appProps("")},
		{"xl/workbook.xml", workbook},
		{"xl/_rels/workbook.xml.rels", wbRels.xml()},
		{"xl/styles.xml", st.xml()},
	}, parts...))
}

// sheetName is a name Excel takes: at most 31 characters, none of []:*?/\, not used yet.
func sheetName(name string, i int, used map[string]bool) string {
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\`, r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	if name == "" {
		name = fmt.Sprintf("Sheet%d", i+1)
	}
	if utf8.RuneCountInString(name) > 31 {
		name = string([]rune(name)[:31])
	}
	base := name
	for n := 2; used[strings.ToLower(name)]; n++ {
		suffix := fmt.Sprintf(" (%d)", n)
		name = string([]rune(base)[:min(utf8.RuneCountInString(base), 31-len(suffix))]) + suffix
	}
	used[strings.ToLower(name)] = true
	return name
}

// grid is a sheet's cells as written: its rows, each cell's style, and the cells formulas read (sheet).
type grid struct {
	rows   [][]gcell
	cols   int
	header bool
	sheet  *xsheet
}

type gcell struct {
	cell  *xcell // nil: empty
	style int
}

func buildGrid(s sheetSpec, st *xlsxStyles) (*grid, error) {
	g := &grid{header: len(s.Header) > 0, sheet: &xsheet{cells: map[[2]int]*xcell{}}}
	var rows [][]any
	if g.header {
		h := make([]any, len(s.Header))
		for i, v := range s.Header {
			h[i] = v
		}
		rows = append(rows, h)
	}
	rows = append(rows, s.Rows...)
	for ri, r := range rows {
		row := make([]gcell, len(r))
		for ci, v := range r {
			header := ri == 0 && g.header
			format := ""
			if ci < len(s.Formats) && !header {
				format = s.Formats[ci]
			}
			c, err := cellOf(v, format)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", cellRef(ci, ri), err)
			}
			row[ci] = gcell{cell: c, style: st.style(header, format)}
			if c != nil {
				g.sheet.cells[[2]int{ci, ri}] = c
			}
		}
		g.rows = append(g.rows, row)
		g.cols = max(g.cols, len(r))
	}
	return g, nil
}

// cellOf is what a JSON value puts in a cell (nil for null).
func cellOf(v any, format string) (*xcell, error) {
	switch v := v.(type) {
	case nil:
		return nil, nil
	case bool:
		return &xcell{v: boolv(v)}, nil
	case json.Number:
		f, err := v.Float64()
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, fmt.Errorf("%s is not a number Excel can hold", v)
		}
		return &xcell{v: num(f)}, nil
	case float64:
		return &xcell{v: num(v)}, nil
	case string:
		if strings.HasPrefix(v, "=") && len(v) > 1 {
			return &xcell{formula: strings.TrimPrefix(v, "=")}, nil
		}
		if isDateFormat(format) {
			if serial, ok := excelDate(v); ok {
				return &xcell{v: num(serial)}, nil
			}
		}
		return &xcell{v: value{kind: 's', str: v}}, nil
	}
	return nil, fmt.Errorf("a cell is text, a number, true / false or null, not %T", v)
}

func (g *grid) xml(book *xbook, s sheetSpec) string {
	widths := make([]float64, g.cols)
	var data strings.Builder
	for ri, row := range g.rows {
		fmt.Fprintf(&data, `<row r="%d">`, ri+1)
		for ci, gc := range row {
			cell, shown := cellXML(book, g.sheet, ci, ri, gc)
			data.WriteString(cell)
			widths[ci] = max(widths[ci], textWidth(shown))
		}
		data.WriteString(`</row>`)
	}
	var b strings.Builder
	b.WriteString(xmlHead + `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">`)
	if len(g.rows) > 0 && g.cols > 0 {
		fmt.Fprintf(&b, `<dimension ref="A1:%s"/>`, cellRef(g.cols-1, len(g.rows)-1))
	}
	freeze := g.header
	if s.Freeze != nil {
		freeze = *s.Freeze && len(g.rows) > 1
	}
	if freeze {
		b.WriteString(`<sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>` +
			`<selection pane="bottomLeft" activeCell="A2" sqref="A2"/></sheetView></sheetViews>`)
	}
	b.WriteString(`<sheetFormatPr defaultRowHeight="15"/>`)
	if g.cols > 0 {
		b.WriteString(`<cols>`)
		for i, w := range widths {
			if i < len(s.Widths) && s.Widths[i] > 0 {
				w = s.Widths[i]
			} else {
				w = math.Min(math.Max(w+2, 8), 60)
			}
			fmt.Fprintf(&b, `<col min="%d" max="%d" width="%.1f" customWidth="1"/>`, i+1, i+1, w)
		}
		b.WriteString(`</cols>`)
	}
	b.WriteString(`<sheetData>` + data.String() + `</sheetData></worksheet>`)
	return b.String()
}

// cellXML is one cell, a formula with its value when it can be worked out, and the words it shows (for its column's
// width).
func cellXML(book *xbook, sh *xsheet, col, row int, gc gcell) (string, string) {
	ref := cellRef(col, row)
	s := ""
	if gc.style > 0 {
		s = fmt.Sprintf(` s="%d"`, gc.style)
	}
	c := gc.cell
	if c == nil {
		if gc.style == 0 {
			return "", ""
		}
		return `<c r="` + ref + `"` + s + `/>`, ""
	}
	v, f := c.v, ""
	if c.formula != "" {
		f = `<f>` + esc(c.formula) + `</f>`
		var ok bool
		if v, ok = book.eval(sh, col, row); !ok {
			return `<c r="` + ref + `"` + s + `>` + f + `</c>`, "0000000000"
		}
		if v.kind == 0 {
			v = num(0)
		}
	}
	switch v.kind {
	case 'b':
		return `<c r="` + ref + `"` + s + ` t="b">` + f + `<v>` + toText(num(v.num)) + `</v></c>`, toText(v)
	case 'n':
		n := strconv.FormatFloat(v.num, 'g', 15, 64)
		return `<c r="` + ref + `"` + s + `>` + f + `<v>` + n + `</v></c>`, n
	case 'e':
		return `<c r="` + ref + `"` + s + ` t="e">` + f + `<v>` + v.str + `</v></c>`, v.str
	}
	if f != "" {
		return `<c r="` + ref + `"` + s + ` t="str">` + f + `<v>` + esc(v.str) + `</v></c>`, v.str
	}
	return `<c r="` + ref + `"` + s + ` t="inlineStr"><is><t xml:space="preserve">` + esc(v.str) + `</t></is></c>`, v.str
}

// cellRef is the A1 name of column c, row r (both from 0).
func cellRef(c, r int) string {
	col := ""
	for c++; c > 0; c = (c - 1) / 26 {
		col = string(rune('A'+(c-1)%26)) + col
	}
	return col + strconv.Itoa(r+1)
}

// textWidth is about how many characters wide s is, a CJK one counting two.
func textWidth(s string) float64 {
	w := 0.0
	for _, line := range strings.Split(s, "\n") {
		n := 0.0
		for _, r := range line {
			if r >= 0x1100 && (r <= 0x115F || r >= 0x2E80 && r <= 0xA4CF || r >= 0xAC00 && r <= 0xD7A3 || r >= 0xF900 && r <= 0xFAFF ||
				r >= 0xFE30 && r <= 0xFE4F || r >= 0xFF00 && r <= 0xFF60 || r >= 0xFFE0 && r <= 0xFFE6 || r >= 0x20000) {
				n += 2
			} else {
				n++
			}
		}
		w = math.Max(w, n)
	}
	return w
}

func isDateFormat(f string) bool {
	f = strings.ToLower(f)
	return strings.Contains(f, "yy") || strings.Contains(f, "dd") || strings.Contains(f, "mmm")
}

// excelDate is an ISO date (and time) as Excel's day number.
func excelDate(v string) (float64, bool) {
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05", time.RFC3339} {
		if t, err := time.Parse(layout, v); err == nil {
			epoch := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC) // as written: no zone
			if t.Year() < 1900 {
				return 0, false
			}
			// Subtract seconds to avoid saturating time.Duration for distant dates.
			serial := float64(t.Unix()-epoch.Unix()) / 86400
			if t.Year() == 1900 && t.Month() < time.March {
				serial-- // Excel reserves serial 60 for the nonexistent 1900-02-29.
			}
			return serial, true
		}
	}
	return 0, false
}

// xlsxStyles are the cell formats used: 0 plain, 1 header, then one per number format (and its header-free use).
type xlsxStyles struct {
	fmts  map[string]int // format code → its xf index
	order []string
}

func (s *xlsxStyles) style(header bool, format string) int {
	if header {
		return 1
	}
	if format == "" {
		return 0
	}
	if i, ok := s.fmts[format]; ok {
		return i
	}
	s.order = append(s.order, format)
	s.fmts[format] = len(s.order) + 1
	return s.fmts[format]
}

func (s *xlsxStyles) xml() string {
	var b strings.Builder
	b.WriteString(xmlHead + `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	if len(s.order) > 0 {
		fmt.Fprintf(&b, `<numFmts count="%d">`, len(s.order))
		for i, f := range s.order {
			fmt.Fprintf(&b, `<numFmt numFmtId="%d" formatCode="%s"/>`, 164+i, esc(f))
		}
		b.WriteString(`</numFmts>`)
	}
	b.WriteString(`<fonts count="2"><font><sz val="11"/><name val="Calibri"/><family val="2"/></font><font><b/><sz val="11"/><name val="Calibri"/><family val="2"/></font></fonts>` +
		`<fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill>` +
		`<fill><patternFill patternType="solid"><fgColor rgb="FFE7ECF3"/><bgColor indexed="64"/></patternFill></fill></fills>` +
		`<borders count="2"><border><left/><right/><top/><bottom/><diagonal/></border>` +
		`<border><left/><right/><top/><bottom style="thin"><color rgb="FFBFBFBF"/></bottom><diagonal/></border></borders>` +
		`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>`)
	fmt.Fprintf(&b, `<cellXfs count="%d"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>`+
		`<xf numFmtId="0" fontId="1" fillId="2" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1"/>`, 2+len(s.order))
	for i := range s.order {
		fmt.Fprintf(&b, `<xf numFmtId="%d" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>`, 164+i)
	}
	b.WriteString(`</cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`)
	return b.String()
}
