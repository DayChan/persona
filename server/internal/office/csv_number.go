package office

import (
	"strconv"
	"strings"
)

// csvNumberExact compares the original decimal with the number cellXML would write.
// Comparing floats here would already have lost the digits CSV needs to preserve.
// Integer literals are often identifiers, so they must survive exactly. Decimals and
// exponents are measurements (a float's shortest form often has 16-17 digits): they may
// round to the 15 significant digits a spreadsheet keeps, but must not change further.
func csvNumberExact(src string, f float64) bool {
	digits, scale, negative, ok := csvDecimal(src)
	if !ok {
		return false
	}
	if strings.ContainsAny(src, ".eE") {
		digits, scale = csvRound(digits, scale, 15)
	}
	written, writtenScale, writtenNegative, ok := csvDecimal(strconv.FormatFloat(f, 'g', 15, 64))
	return ok && digits == written && scale == writtenScale && negative == writtenNegative
}

// csvDecimal represents a decimal as signed digits times 10^scale, ignoring
// insignificant zeros. Exponents are bounded by input length and the float64 range;
// even hostile exponent strings require only a scan, never a power-of-ten allocation.
func csvDecimal(src string) (digits string, scale int, negative, ok bool) {
	limit := len(src) + 400
	if len(src) > 0 && (src[0] == '-' || src[0] == '+') {
		negative = src[0] == '-'
		src = src[1:]
	}
	if i := strings.IndexAny(src, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(src[i+1:])
		if err != nil || exponent < -limit || exponent > limit {
			return "", 0, false, false
		}
		scale = exponent
		src = src[:i]
	}
	if i := strings.IndexByte(src, '.'); i >= 0 {
		scale -= len(src) - i - 1
		src = src[:i] + src[i+1:]
	}
	if src == "" {
		return "", 0, false, false
	}
	for i := range len(src) {
		if src[i] < '0' || src[i] > '9' {
			return "", 0, false, false
		}
	}
	src = strings.TrimLeft(src, "0")
	if src == "" {
		return "0", 0, false, true
	}
	digits = strings.TrimRight(src, "0")
	scale += len(src) - len(digits)
	return digits, scale, negative, true
}

// csvRound rounds a csvDecimal (no leading or trailing zeros) half up to n significant digits.
func csvRound(digits string, scale, n int) (string, int) {
	if len(digits) <= n {
		return digits, scale
	}
	b := []byte(digits[:n])
	scale += len(digits) - n
	carry := digits[n] >= '5'
	for i := n - 1; carry && i >= 0; i-- {
		if b[i] == '9' {
			b[i] = '0'
		} else {
			b[i]++
			carry = false
		}
	}
	if carry {
		b = append([]byte{'1'}, b...)
	}
	rounded := strings.TrimRight(string(b), "0")
	return rounded, scale + len(b) - len(rounded)
}
