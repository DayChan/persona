package office

import (
	"encoding/xml"
	"fmt"
	"math"
	"testing"
)

func TestXlsxDateSerials(t *testing.T) {
	for _, tc := range []struct {
		input  string
		serial float64
		text   bool
	}{
		{"0000-01-01", 0, true},
		{"1800-01-01", 0, true},
		{"1899-12-31", 0, true},
		{"1899-12-31 23:59:59", 0, true},
		{"1899-12-31T23:59:59-12:00", 0, true},
		{"1900-01-01", 1, false},
		{"1900-01-01T00:30:00+09:00", 1 + 1800.0/86400, false},
		{"1900-02-01", 32, false},
		{"1900-02-28", 59, false},
		{"1900-02-28T23:59:59Z", 59 + 86399.0/86400, false},
		{"1900-02-29", 0, true},
		{"1900-03-01", 61, false},
		{"1900-03-01T00:30:00-09:00", 61 + 1800.0/86400, false},
		{"2000-01-01", 36526, false},
		{"2000-02-29", 36585, false},
		{"2192-04-09", 106752, false},
		{"2200-01-01", 109575, false},
		{"2400-02-29", 182682, false},
		{"9999-12-31", 2958465, false},
		{"2200-01-01 12:34", 109575 + 45240.0/86400, false},
		{"2200-01-01T12:34:56+09:00", 109575 + 45296.0/86400, false},
		{"2200-02-29", 0, true},
		{"not a date", 0, true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			src := fmt.Sprintf(`{"sheets":[{"rows":[[%q,%q]],"formats":["yyyy-mm-dd hh:mm:ss"]}]}`, tc.input, tc.input)
			data, err := Xlsx([]byte(src), "")
			if err != nil {
				t.Fatal(err)
			}
			var sheet struct {
				Cells []struct {
					Type   string  `xml:"t,attr"`
					Number float64 `xml:"v"`
					Text   string  `xml:"is>t"`
				} `xml:"sheetData>row>c"`
			}
			if err := xml.Unmarshal([]byte(parts(t, data)["xl/worksheets/sheet1.xml"]), &sheet); err != nil {
				t.Fatal(err)
			}
			if len(sheet.Cells) != 2 {
				t.Fatalf("got %d cells, want formatted and unformatted controls", len(sheet.Cells))
			}
			formatted, plain := sheet.Cells[0], sheet.Cells[1]
			if tc.text {
				if formatted.Type != "inlineStr" || formatted.Text != tc.input {
					t.Errorf("invalid date changed: %+v", formatted)
				}
			} else if formatted.Type != "" || math.Abs(formatted.Number-tc.serial) > 1e-9 {
				t.Errorf("date cell %+v, want serial %.12f", formatted, tc.serial)
			}
			if plain.Type != "inlineStr" || plain.Text != tc.input {
				t.Errorf("unformatted text changed: %+v", plain)
			}
		})
	}
}
