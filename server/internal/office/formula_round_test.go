package office

import (
	"encoding/json"
	"encoding/xml"
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestRoundCachedValues(t *testing.T) {
	for _, tc := range []struct{ formula, want string }{
		{"ROUND(1.25,309)", "1.25"},
		{"ROUND(1.25,-400)", "0"},
		{"ROUND(-1.25,309)", "-1.25"},
		{"ROUND(-1.25,-400)", "0"},
		{"ROUND(0,309)", "0"},
		{"ROUND(0,-400)", "0"},
		{"ROUND(1.25,1e100)", "1.25"},
		{"ROUND(1.25,-1e100)", "0"},
		{"ROUND(1e300,20)", "1e300"},
		{"ROUND(-1e300,20)", "-1e300"},
		{"ROUND(1.25e-309,309)", "1e-309"},
		{"ROUND(-1.25e-309,309)", "-1e-309"},
		{"ROUND(1.25e-310,309)", "0"},
		{"ROUND(1e-320,309)", "0"},
		{"ROUND(-1e-320,309)", "0"},
		{"ROUND(2.5e-309,309)", "3e-309"},
		{"ROUND(1.25e-320,320)", "1e-320"},
		{"ROUND(5e-324,323)", "0"},
		{"ROUND(5e-324,324)", "5e-324"},
		{"ROUND(1.25,308)", "1.25"},
		{"ROUND(5e307,-308)", "1e308"},
		{"ROUND(1.7e308,-309)", "0"},
		{"ROUND(1.7e308,-308)", "#NUM!"},
		{"IFERROR(ROUND(1.7e308,-308),7)", "7"},
		{"IFERROR(ROUND(1.25,309),7)", "1.25"},
		{"IFERROR(ROUND(1.25,-400),7)", "0"},
		{"ROUND(2.345,2)", "2.35"},
		{"ROUND(2.3449999999999998,2)", "2.35"},
		{"ROUND(-1.475,2)", "-1.48"},
		{"ROUND(21.5,-1)", "20"},
		{"ROUND(626.3,-3)", "1000"},
		{"ROUND(2.5,0)", "3"},
		{"ROUND(-2.5,0)", "-3"},
		{"ROUND(1.234,2.9)", "1.23"},
		{`ROUND("1.25",2)`, "1.25"},
		{`ROUND("NaN",2)`, "#VALUE!"},
		{`IFERROR(ROUND("bad",2),7)`, "7"},
	} {
		t.Run(tc.formula, func(t *testing.T) {
			src, err := json.Marshal(workbookSpec{Sheets: []sheetSpec{{Rows: [][]any{{"=" + tc.formula}}}}})
			if err != nil {
				t.Fatal(err)
			}
			data, err := Xlsx(src, "")
			if err != nil {
				t.Fatal(err)
			}
			var sheet struct {
				Cell struct {
					Type    string `xml:"t,attr"`
					Formula string `xml:"f"`
					Value   string `xml:"v"`
				} `xml:"sheetData>row>c"`
			}
			if err := xml.Unmarshal([]byte(parts(t, data)["xl/worksheets/sheet1.xml"]), &sheet); err != nil {
				t.Fatal(err)
			}
			c := sheet.Cell
			if c.Formula != tc.formula {
				t.Fatalf("cached formula %q, want %q", c.Formula, tc.formula)
			}
			if strings.HasPrefix(tc.want, "#") {
				if c.Type != "e" || c.Value != tc.want {
					t.Fatalf("cache type %q value %q, want error %s", c.Type, c.Value, tc.want)
				}
				return
			}
			got, err := strconv.ParseFloat(c.Value, 64)
			want, _ := strconv.ParseFloat(tc.want, 64)
			// The writer stores 15 significant digits; subnormal results also have a float64 quantum.
			tolerance := math.Max(math.Abs(want)*1e-14, math.SmallestNonzeroFloat64)
			if math.Abs(want) == math.SmallestNonzeroFloat64 {
				tolerance = 0 // preserving the smallest nonzero value must not accept zero
			}
			if c.Type != "" || err != nil || math.IsNaN(got) || math.IsInf(got, 0) ||
				want == 0 && got != 0 || math.Abs(got-want) > tolerance {
				t.Fatalf("cache type %q value %q, want finite %s", c.Type, c.Value, tc.want)
			}
		})
	}
}
