package office

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestXlsxFromCSVExactNumbers(t *testing.T) {
	cases := []struct {
		name, input, number string // Empty number means the exact input must remain text.
	}{
		{"long integer", "1234567890123456", ""},
		{"negative long integer", "-1234567890123456", ""},
		{"long identifier", "123456789012345678", ""},
		{"negative long identifier", "-123456789012345678", ""},
		{"beyond binary precision", "9007199254740993", ""},
		{"binary exact but serializer rounds", "9007199254740992", ""},
		{"count", "42", "42"},
		{"negative count", "-42", "-42"},
		{"positive sign", "+42", "42"},
		{"zero", "0", "0"},
		{"negative zero", "-0", "-0"},
		{"fifteen significant digits", "123456789012345", "123456789012345"},
		{"sixteen digits with trailing zero", "1234567890123450", "1.23456789012345e+15"},
		{"power of ten", "1000000000000000", "1e+15"},
		{"fraction", "0.1", "0.1"},
		{"fraction without integer part", ".5", "0.5"},
		{"decimal without fractional part", "5.", "5"},
		{"signed decimal zero", "+0.000", "0"},
		{"decimal trailing zeros", "3.500", "3.5"},
		{"equivalent scientific decimal", "3.5e0", "3.5"},
		{"equivalent scaled decimal", "3500e-3", "3.5"},
		{"decimal precision boundary", "1.23456789012345", "1.23456789012345"},
		{"decimal rounds to spreadsheet precision", "1.234567890123456", "1.23456789012346"},
		{"negative decimal rounds", "-1.234567890123456", "-1.23456789012346"},
		{"decimal rounds to integer", "1.0000000000000001", "1"},
		{"float repr one third", "0.3333333333333333", "0.333333333333333"},
		{"float repr sum", "0.30000000000000004", "0.3"},
		{"float repr coordinate", "-122.41941550000001", "-122.4194155"},
		{"decimal rounds up through nines", "9.9999999999999999", "10"},
		{"scientific rounds", "1.234567890123456e15", "1.23456789012346e+15"},
		{"negative scientific rounds", "-1.234567890123456e+17", "-1.23456789012346e+17"},
		{"scientific exact", "1.2345678901234500E+17", "1.23456789012345e+17"},
		{"small scientific exact", "1.23456789012345e-30", "1.23456789012345e-30"},
		{"small scientific rounds", "1.234567890123456e-30", "1.23456789012346e-30"},
		{"large finite exact", "1e308", "1e+308"},
		{"largest finite rounds", "1.7976931348623157e308", "1.79769313486232e+308"},
		{"small finite exact", "1e-308", "1e-308"},
		{"subnormal changes value", "5e-324", ""},
		{"underflows silently", "1e-324", ""},
		{"negative underflow", "-1e-400", ""},
		{"bounded negative exponent", "1e-1000000000", ""},
		{"overflow", "1e309", ""},
		{"enormous exponent", "1e" + strings.Repeat("9", 4096), ""},
		{"enormous negative exponent", "1e-" + strings.Repeat("9", 4096), ""},
		{"long equivalent decimal", "1" + strings.Repeat("0", 4096) + "e-4096", "1"},
		{"distant fractional digit", "1." + strings.Repeat("0", 4096) + "1", "1"},
		{"leading zero", "007", ""},
		{"negative leading zero", "-007", ""},
		{"positive leading zero", "+007", ""},
		{"nonfinite", "NaN", ""},
		{"positive infinity", "+Inf", ""},
		{"negative infinity", "-Infinity", ""},
		{"plain text", "sample & <text>", ""},
		{"whitespace text", " 42 ", ""},
		{"date text", "2026-01-31", ""},
		{"empty text", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var src bytes.Buffer
			w := csv.NewWriter(&src)
			if err := w.WriteAll([][]string{{"Value", "Count"}, {tc.input, "42"}}); err != nil {
				t.Fatal(err)
			}
			data, err := XlsxFromCSV(src.Bytes(), "Data", "Synthetic numbers")
			if err != nil {
				t.Fatal(err)
			}
			p := parts(t, data) // Also verifies ZIP and all XML parts.
			sheet, cells := csvWorksheet(t, p["xl/worksheets/sheet1.xml"])
			value, count := cells["A2"], cells["B2"]
			if tc.number == "" {
				if value.Type != "inlineStr" || value.Text != tc.input || value.Number != "" {
					t.Errorf("exact CSV text changed: type=%q number=%q text=%q", value.Type, value.Number, value.Text)
				}
			} else if value.Type != "" || value.Number != tc.number || value.Text != "" {
				t.Errorf("numeric cell = %+v, want %s", value, tc.number)
			}
			if count.Type != "" || count.Number != "42" || count.Style != "" {
				t.Errorf("ordinary numeric control changed: %+v", count)
			}
			if sheet.Dimension.Ref != "A1:B2" || len(sheet.Rows) != 2 || len(cells) != 4 || value.Style != "" {
				t.Errorf("worksheet shape or data style changed: %+v", sheet)
			}
			for _, ref := range []string{"A1", "B1"} {
				if cells[ref].Style != "1" || cells[ref].Type != "inlineStr" {
					t.Errorf("header style changed at %s: %+v", ref, cells[ref])
				}
			}
			if !strings.Contains(p["xl/worksheets/sheet1.xml"], `state="frozen"`) || !strings.Contains(p["xl/styles.xml"], `<cellXfs count="2">`) {
				t.Error("default freeze or workbook styles changed")
			}
		})
	}
}

type csvTestCell struct {
	Ref     string `xml:"r,attr"`
	Type    string `xml:"t,attr"`
	Style   string `xml:"s,attr"`
	Number  string `xml:"v"`
	Text    string `xml:"is>t"`
	Formula string `xml:"f"`
}

type csvTestSheet struct {
	Dimension struct {
		Ref string `xml:"ref,attr"`
	} `xml:"dimension"`
	Rows []struct {
		Cells []csvTestCell `xml:"c"`
	} `xml:"sheetData>row"`
}

func csvWorksheet(t *testing.T, src string) (csvTestSheet, map[string]csvTestCell) {
	t.Helper()
	var sheet csvTestSheet
	if err := xml.Unmarshal([]byte(src), &sheet); err != nil {
		t.Fatal(err)
	}
	cells := make(map[string]csvTestCell)
	for _, row := range sheet.Rows {
		for _, c := range row.Cells {
			cells[c.Ref] = c
		}
	}
	return sheet, cells
}

func TestCSVOfficeCommandPreservesTextAndFormulas(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "synthetic.csv")
	output := filepath.Join(dir, "synthetic.xlsx")
	// BOM, quoted text, ragged rows and empty cells retain their existing CSV behavior.
	src := "\xef\xbb\xbfIdentifier,Count,Formula,Note\n123456789012345678,42,=B2*2,\"sample, & <text>\"\n007,3.500\n-1234567890123456,,=SUM(B2:B3),\n"
	var stdout, stderr bytes.Buffer
	if err := os.WriteFile(input, []byte("Identifier,Count\n\"unterminated,42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := Main([]string{"xlsx", input, "-o", output}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "the CSV") || stdout.Len() != 0 {
		t.Fatalf("malformed CSV: exit %d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("malformed CSV produced a workbook: %v", err)
	}
	// Correcting the input and retrying writes a complete, exact workbook.
	if err := os.WriteFile(input, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := Main([]string{"xlsx", input, "-o", output}, &stdout, &stderr); code != 0 {
		t.Fatalf("office xlsx exit %d: %s", code, stderr.String())
	}
	var result struct {
		Bytes int `json:"bytes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Bytes == 0 {
		t.Fatalf("invalid command result %s: %v", stdout.String(), err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	p := parts(t, data)
	sheet, cells := csvWorksheet(t, p["xl/worksheets/sheet1.xml"])
	for ref, want := range map[string]string{"A2": "123456789012345678", "A3": "007", "A4": "-1234567890123456", "D2": "sample, & <text>"} {
		if cells[ref].Type != "inlineStr" || cells[ref].Text != want {
			t.Errorf("%s lost text: %+v", ref, cells[ref])
		}
	}
	for ref, want := range map[string]string{"B2": "42", "B3": "3.5", "C2": "84", "C4": "45.5"} {
		if cells[ref].Type != "" || cells[ref].Number != want {
			t.Errorf("%s lost numeric value: %+v", ref, cells[ref])
		}
	}
	if cells["C2"].Formula != "B2*2" || cells["C4"].Formula != "SUM(B2:B3)" {
		t.Error("CSV formulas changed")
	}
	if _, present := cells["C3"]; present || sheet.Dimension.Ref != "A1:D4" || len(cells) != 14 {
		t.Errorf("ragged row or worksheet dimension changed: %+v", sheet)
	}
	info := check(t, data, ".xlsx")
	first := info["sheets"].([]any)[0].(map[string]any)
	if first["range"] != "A1:D4" || first["formulas"] != 2.0 || fmt.Sprint(first["first_row"]) != "[Identifier Count Formula Note]" {
		t.Errorf("workbook check = %v", info)
	}
}
