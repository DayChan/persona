package office

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// A workbook's formulas worked out when it is written, so the file carries their values the way Excel saves them:
// viewers that do not calculate (QuickLook, the app's preview, many web viewers) show the totals instead of empty
// cells. Excel and Numbers still calculate everything again on opening (fullCalcOnLoad). What is worked out:
// + - * / ^ & % and comparisons, cell and range references (on other sheets too: Sheet!A1, 'My sheet'!A1:B2), and
// SUM AVERAGE MIN MAX COUNT COUNTA ROUND ABS IF IFERROR AND OR NOT. A formula with anything else is left without a
// value (errUnsupported), for Excel to work out.

type value struct {
	kind byte // 'n' number, 's' text, 'b' true / false, 'e' an error, 0 blank
	num  float64
	str  string // text, or an error's name (#DIV/0!)
}

var errUnsupported = errors.New("not worked out here")

func num(f float64) value { return value{kind: 'n', num: f} }
func errv(s string) value { return value{kind: 'e', str: s} }
func boolv(b bool) value  { return value{kind: 'b', num: b2f(b)} }
func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// xcell is a cell of the workbook being written.
type xcell struct {
	v       value  // a constant's value, or a formula's once worked out
	formula string // without its =
	state   byte   // a formula's: 0 not yet, 1 working it out, 2 done, 3 cannot
}

type xsheet struct {
	cells map[[2]int]*xcell // [col, row] from 0
}

type xbook struct {
	sheets map[string]*xsheet // by lower-cased name
}

// eval is the value of a cell, working its formula out the first time; ok false when it cannot be worked out here.
func (b *xbook) eval(sh *xsheet, col, row int) (value, bool) {
	c := sh.cells[[2]int{col, row}]
	if c == nil {
		return value{}, true
	}
	if c.formula == "" {
		return c.v, true
	}
	switch c.state {
	case 2:
		return c.v, true
	case 3:
		return value{}, false
	case 1:
		c.v, c.state = errv("#REF!"), 2 // a circular reference
		return c.v, true
	}
	c.state = 1
	e, err := parseFormula(c.formula)
	if err != nil {
		c.state = 3
		return value{}, false
	}
	v, err := e.eval(b, sh)
	if err != nil {
		c.state = 3
		return value{}, false
	}
	if v.kind == 'r' {
		v = errv("#VALUE!")
	}
	c.v, c.state = v, 2
	return c.v, true
}

// An expression, worked out on a sheet. A range is kind 'r' (only a function reads its cells, refExpr.cells).
type expr interface {
	eval(b *xbook, sh *xsheet) (value, error)
}

type (
	litExpr struct{ v value }
	refExpr struct {
		sheet          string
		c1, r1, c2, r2 int
		isRange        bool
	}
	unaryExpr struct {
		op byte
		x  expr
	}
	binExpr struct {
		op   string
		l, r expr
	}
	callExpr struct {
		name string
		args []expr
	}
)

func (e litExpr) eval(*xbook, *xsheet) (value, error) { return e.v, nil }

func (e refExpr) sheetOf(b *xbook, sh *xsheet) *xsheet {
	if e.sheet == "" {
		return sh
	}
	return b.sheets[strings.ToLower(e.sheet)]
}

func (e refExpr) eval(b *xbook, sh *xsheet) (value, error) {
	target := e.sheetOf(b, sh)
	if target == nil {
		return errv("#REF!"), nil
	}
	if !e.isRange {
		v, ok := b.eval(target, e.c1, e.r1)
		if !ok {
			return value{}, errUnsupported
		}
		return v, nil
	}
	return value{kind: 'r'}, nil // ranges are only read by functions (cells)
}

// cells are a range's (or a single reference's) values.
func (e refExpr) cells(b *xbook, sh *xsheet) ([]value, error) {
	target := e.sheetOf(b, sh)
	if target == nil {
		return []value{errv("#REF!")}, nil
	}
	var out []value
	for c := min(e.c1, e.c2); c <= max(e.c1, e.c2); c++ {
		for r := min(e.r1, e.r2); r <= max(e.r1, e.r2); r++ {
			v, ok := b.eval(target, c, r)
			if !ok {
				return nil, errUnsupported
			}
			out = append(out, v)
		}
	}
	return out, nil
}

func (e unaryExpr) eval(b *xbook, sh *xsheet) (value, error) {
	v, err := e.x.eval(b, sh)
	if err != nil || v.kind == 'e' {
		return v, err
	}
	n, ok := toNum(v)
	if !ok {
		return errv("#VALUE!"), nil
	}
	if e.op == '-' {
		return num(-n), nil
	}
	return num(n / 100), nil // %
}

func (e binExpr) eval(b *xbook, sh *xsheet) (value, error) {
	l, err := e.l.eval(b, sh)
	if err != nil {
		return l, err
	}
	r, err := e.r.eval(b, sh)
	if err != nil {
		return r, err
	}
	if l.kind == 'r' || r.kind == 'r' {
		return errv("#VALUE!"), nil
	}
	if l.kind == 'e' {
		return l, nil
	}
	if r.kind == 'e' {
		return r, nil
	}
	switch e.op {
	case "&":
		return value{kind: 's', str: toText(l) + toText(r)}, nil
	case "=", "<>", "<", ">", "<=", ">=":
		c := compare(l, r)
		return boolv(map[string]bool{"=": c == 0, "<>": c != 0, "<": c < 0, ">": c > 0, "<=": c <= 0, ">=": c >= 0}[e.op]), nil
	}
	x, ok1 := toNum(l)
	y, ok2 := toNum(r)
	if !ok1 || !ok2 {
		return errv("#VALUE!"), nil
	}
	switch e.op {
	case "+":
		return num(x + y), nil
	case "-":
		return num(x - y), nil
	case "*":
		return num(x * y), nil
	case "/":
		if y == 0 {
			return errv("#DIV/0!"), nil
		}
		return num(x / y), nil
	case "^":
		p := math.Pow(x, y)
		if math.IsNaN(p) || math.IsInf(p, 0) {
			return errv("#NUM!"), nil
		}
		return num(p), nil
	}
	return value{}, errUnsupported
}

func toNum(v value) (float64, bool) {
	switch v.kind {
	case 'n', 'b':
		return v.num, true
	case 0:
		return 0, true
	case 's':
		f, err := strconv.ParseFloat(strings.TrimSpace(v.str), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

func toText(v value) string {
	switch v.kind {
	case 'n':
		return strconv.FormatFloat(v.num, 'g', 15, 64)
	case 'b':
		if v.num != 0 {
			return "TRUE"
		}
		return "FALSE"
	}
	return v.str
}

// compare orders two values as Excel does: numbers, then text (ignoring case), then true / false.
func compare(a, b value) int {
	rank := func(v value) int {
		switch v.kind {
		case 's':
			return 1
		case 'b':
			return 2
		}
		return 0 // numbers and blanks
	}
	if ra, rb := rank(a), rank(b); ra != rb {
		if a.kind == 0 || b.kind == 0 { // a blank is 0, "" or FALSE, whatever it meets
			ta, tb := toText(a), toText(b)
			if a.kind == 0 && b.kind == 's' || b.kind == 0 && a.kind == 's' {
				return strings.Compare(strings.ToLower(ta), strings.ToLower(tb))
			}
			return cmpFloat(a.num, b.num)
		}
		return ra - rb
	}
	if a.kind == 's' {
		return strings.Compare(strings.ToLower(a.str), strings.ToLower(b.str))
	}
	return cmpFloat(a.num, b.num)
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// args are a call's arguments, a range spread into its cells; each with whether it came from a reference.
func (e callExpr) args1(b *xbook, sh *xsheet) ([]value, []bool, error) {
	var out []value
	var fromRef []bool
	for _, a := range e.args {
		if r, ok := a.(refExpr); ok {
			cells, err := r.cells(b, sh)
			if err != nil {
				return nil, nil, err
			}
			for _, c := range cells {
				out, fromRef = append(out, c), append(fromRef, true)
			}
			continue
		}
		v, err := a.eval(b, sh)
		if err != nil {
			return nil, nil, err
		}
		out, fromRef = append(out, v), append(fromRef, false)
	}
	return out, fromRef, nil
}

func (e callExpr) eval(b *xbook, sh *xsheet) (value, error) {
	switch e.name {
	case "IF":
		if len(e.args) < 2 || len(e.args) > 3 {
			return errv("#VALUE!"), nil
		}
		c, err := e.args[0].eval(b, sh)
		if err != nil || c.kind == 'e' {
			return c, err
		}
		t, ok := toNum(c)
		if !ok {
			return errv("#VALUE!"), nil
		}
		if t != 0 {
			return e.args[1].eval(b, sh)
		}
		if len(e.args) == 3 {
			return e.args[2].eval(b, sh)
		}
		return boolv(false), nil
	case "IFERROR":
		if len(e.args) != 2 {
			return errv("#VALUE!"), nil
		}
		v, err := e.args[0].eval(b, sh)
		if err != nil {
			return v, err
		}
		if v.kind == 'e' {
			return e.args[1].eval(b, sh)
		}
		return v, nil
	}
	vals, fromRef, err := e.args1(b, sh)
	if err != nil {
		return value{}, err
	}
	for _, v := range vals {
		if v.kind == 'e' && e.name != "COUNT" && e.name != "COUNTA" {
			return v, nil
		}
	}
	// The numbers among the arguments: from a reference only numbers count; typed in, text that reads as one does.
	nums := func() ([]float64, value, bool) {
		var out []float64
		for i, v := range vals {
			switch {
			case v.kind == 'n':
				out = append(out, v.num)
			case fromRef[i]:
			case v.kind == 'b':
				out = append(out, v.num)
			case v.kind == 's':
				f, ok := toNum(v)
				if !ok {
					return nil, errv("#VALUE!"), false
				}
				out = append(out, f)
			}
		}
		return out, value{}, true
	}
	switch e.name {
	case "SUM", "AVERAGE", "MIN", "MAX":
		ns, bad, ok := nums()
		if !ok {
			return bad, nil
		}
		total := 0.0
		for _, n := range ns {
			total += n
		}
		switch e.name {
		case "SUM":
			return num(total), nil
		case "AVERAGE":
			if len(ns) == 0 {
				return errv("#DIV/0!"), nil
			}
			return num(total / float64(len(ns))), nil
		}
		if len(ns) == 0 {
			return num(0), nil
		}
		m := ns[0]
		for _, n := range ns[1:] {
			if e.name == "MIN" {
				m = math.Min(m, n)
			} else {
				m = math.Max(m, n)
			}
		}
		return num(m), nil
	case "COUNT", "COUNTA":
		n := 0
		for i, v := range vals {
			// COUNT: numbers; typed in, true / false and text that reads as a number too.
			_, numeric := toNum(v)
			typedIn := !fromRef[i] && (v.kind == 'b' || v.kind == 's' && numeric)
			if e.name == "COUNT" && (v.kind == 'n' || typedIn) || e.name == "COUNTA" && v.kind != 0 {
				n++
			}
		}
		return num(float64(n)), nil
	case "ROUND", "ABS":
		want := map[string]int{"ROUND": 2, "ABS": 1}[e.name]
		if len(e.args) != want || len(vals) != want {
			return errv("#VALUE!"), nil
		}
		x, ok := toNum(vals[0])
		if !ok {
			return errv("#VALUE!"), nil
		}
		if e.name == "ABS" {
			return num(math.Abs(x)), nil
		}
		d, ok := toNum(vals[1])
		if !ok {
			return errv("#VALUE!"), nil
		}
		d = math.Trunc(d)
		// Beyond these places no finite float64 changes, or every finite value rounds to zero.
		if d > 323 {
			return num(x), nil
		}
		if d < -308 {
			return num(0), nil
		}
		// Half away from zero on the number as Excel sees it (15 digits): 2.345 rounds to 2.35, not 2.34.
		// Split large positive powers so tiny inputs can still be rounded without an infinite scale.
		p, extra := math.Pow(10, math.Min(d, 308)), math.Pow(10, math.Max(d-308, 0))
		scaled, _ := strconv.ParseFloat(strconv.FormatFloat(x*p*extra, 'g', 15, 64), 64)
		if math.IsInf(scaled, 0) {
			return num(x), nil // the rounding place is already below this number's precision
		}
		rounded := math.Round(scaled) / p / extra
		if math.IsInf(rounded, 0) {
			return errv("#NUM!"), nil
		}
		return num(rounded), nil
	case "AND", "OR":
		acc, judged := e.name == "AND", false
		for i, v := range vals {
			if fromRef[i] && (v.kind == 0 || v.kind == 's') {
				continue // a reference's blanks and text are skipped
			}
			n, ok := toNum(v)
			if !ok {
				return errv("#VALUE!"), nil
			}
			judged = true
			if e.name == "AND" {
				acc = acc && n != 0
			} else {
				acc = acc || n != 0
			}
		}
		if !judged {
			return errv("#VALUE!"), nil
		}
		return boolv(acc), nil
	case "NOT":
		if len(vals) != 1 {
			return errv("#VALUE!"), nil
		}
		n, ok := toNum(vals[0])
		if !ok {
			return errv("#VALUE!"), nil
		}
		return boolv(n == 0), nil
	}
	return value{}, errUnsupported
}

// parseFormula reads a formula (without its =).
func parseFormula(s string) (expr, error) {
	p := &fparser{s: []rune(s)}
	e, err := p.expr(0)
	if err != nil {
		return nil, err
	}
	p.space()
	if p.i != len(p.s) {
		return nil, errUnsupported
	}
	return e, nil
}

type fparser struct {
	s []rune
	i int
}

func (p *fparser) space() {
	for p.i < len(p.s) && unicode.IsSpace(p.s[p.i]) {
		p.i++
	}
}

func (p *fparser) peek(t string) bool {
	p.space()
	return strings.HasPrefix(string(p.s[p.i:]), t)
}

// binary operators by how tightly they bind
var binOps = []struct {
	op   string
	prec int
}{{"<=", 1}, {">=", 1}, {"<>", 1}, {"=", 1}, {"<", 1}, {">", 1}, {"&", 2}, {"+", 3}, {"-", 3}, {"*", 4}, {"/", 4}, {"^", 5}}

func (p *fparser) expr(min int) (expr, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for {
		found := false
		for _, o := range binOps {
			if o.prec >= min && p.peek(o.op) {
				p.i += len([]rune(o.op))
				r, err := p.expr(o.prec + 1) // all left to right, ^ too (=2^3^2 is 64)
				if err != nil {
					return nil, err
				}
				l, found = binExpr{op: o.op, l: l, r: r}, true
				break
			}
		}
		if !found {
			return l, nil
		}
	}
}

func (p *fparser) unary() (expr, error) {
	if p.peek("-") {
		p.i++
		x, err := p.unary()
		return unaryExpr{op: '-', x: x}, err
	}
	if p.peek("+") {
		p.i++
		return p.unary()
	}
	x, err := p.primary()
	if err != nil {
		return nil, err
	}
	for p.peek("%") {
		p.i++
		x = unaryExpr{op: '%', x: x}
	}
	return x, nil
}

func (p *fparser) primary() (expr, error) {
	p.space()
	if p.i >= len(p.s) {
		return nil, errUnsupported
	}
	c := p.s[p.i]
	switch {
	case c == '(':
		p.i++
		e, err := p.expr(0)
		if err != nil || !p.peek(")") {
			return nil, errUnsupported
		}
		p.i++
		return e, nil
	case c == '"':
		var b strings.Builder
		for p.i++; p.i < len(p.s); p.i++ {
			if p.s[p.i] == '"' {
				if p.i+1 < len(p.s) && p.s[p.i+1] == '"' {
					b.WriteRune('"')
					p.i++
					continue
				}
				p.i++
				return litExpr{value{kind: 's', str: b.String()}}, nil
			}
			b.WriteRune(p.s[p.i])
		}
		return nil, errUnsupported
	case c >= '0' && c <= '9' || c == '.':
		start := p.i
		for p.i < len(p.s) && (p.s[p.i] >= '0' && p.s[p.i] <= '9' || p.s[p.i] == '.' ||
			(p.s[p.i] == 'e' || p.s[p.i] == 'E') && p.i+1 < len(p.s) ||
			(p.s[p.i] == '+' || p.s[p.i] == '-') && p.i > start && (p.s[p.i-1] == 'e' || p.s[p.i-1] == 'E')) {
			p.i++
		}
		f, err := strconv.ParseFloat(string(p.s[start:p.i]), 64)
		if err != nil {
			return nil, errUnsupported
		}
		return litExpr{num(f)}, nil
	}
	sheet := ""
	if c == '\'' {
		end := p.i + 1
		for end < len(p.s) && !(p.s[end] == '\'' && (end+1 >= len(p.s) || p.s[end+1] != '\'')) {
			if p.s[end] == '\'' {
				end++
			}
			end++
		}
		if end+1 >= len(p.s) || p.s[end+1] != '!' {
			return nil, errUnsupported
		}
		sheet = strings.ReplaceAll(string(p.s[p.i+1:end]), "''", "'")
		p.i = end + 2
	}
	start := p.i
	for p.i < len(p.s) && (unicode.IsLetter(p.s[p.i]) || unicode.IsDigit(p.s[p.i]) || p.s[p.i] == '_' || p.s[p.i] == '$' || p.s[p.i] == '.') {
		p.i++
	}
	word := string(p.s[start:p.i])
	if word == "" {
		return nil, errUnsupported
	}
	if sheet == "" && p.i < len(p.s) && p.s[p.i] == '!' {
		sheet, p.i = word, p.i+1
		start = p.i
		for p.i < len(p.s) && (unicode.IsLetter(p.s[p.i]) || unicode.IsDigit(p.s[p.i]) || p.s[p.i] == '$') {
			p.i++
		}
		word = string(p.s[start:p.i])
	}
	if sheet == "" && p.peek("(") {
		p.i++
		call := callExpr{name: strings.ToUpper(word)}
		if !p.peek(")") {
			for {
				a, err := p.expr(0)
				if err != nil {
					return nil, err
				}
				call.args = append(call.args, a)
				if p.peek(",") {
					p.i++
					continue
				}
				break
			}
		}
		if !p.peek(")") {
			return nil, errUnsupported
		}
		p.i++
		return call, nil
	}
	if sheet == "" {
		switch strings.ToUpper(word) {
		case "TRUE":
			return litExpr{boolv(true)}, nil
		case "FALSE":
			return litExpr{boolv(false)}, nil
		}
	}
	c1, r1, ok := parseRef(word)
	if !ok {
		return nil, errUnsupported // a name, or a whole column / row
	}
	ref := refExpr{sheet: sheet, c1: c1, r1: r1, c2: c1, r2: r1}
	if p.i < len(p.s) && p.s[p.i] == ':' {
		p.i++
		start := p.i
		for p.i < len(p.s) && (unicode.IsLetter(p.s[p.i]) || unicode.IsDigit(p.s[p.i]) || p.s[p.i] == '$') {
			p.i++
		}
		c2, r2, ok := parseRef(string(p.s[start:p.i]))
		if !ok {
			return nil, errUnsupported
		}
		ref.c2, ref.r2, ref.isRange = c2, r2, true
	}
	return ref, nil
}

// parseRef reads A1 (or $A$1): the column and row from 0.
func parseRef(s string) (int, int, bool) {
	s = strings.ReplaceAll(strings.ToUpper(s), "$", "")
	i := 0
	for i < len(s) && s[i] >= 'A' && s[i] <= 'Z' {
		i++
	}
	if i == 0 || i > 3 || i == len(s) {
		return 0, 0, false
	}
	row, err := strconv.Atoi(s[i:])
	if err != nil || row < 1 {
		return 0, 0, false
	}
	return colNumber(s[:i]) - 1, row - 1, true
}
