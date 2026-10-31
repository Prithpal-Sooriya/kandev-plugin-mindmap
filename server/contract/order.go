package contract

import "strings"

// Fractional-index order keys. A key is a non-empty base-62 string over
// OrderDigits that does not end in '0'. Keys compare byte-wise, and the digit
// alphabet is in ASCII order, so string comparison is numeric comparison of
// the fraction 0.<key>. OrderBetween always finds a key strictly between two
// keys, so inserting never renumbers siblings. ui/src/contract/order.ts is the
// same algorithm; contract/fixtures/OrderCases.json pins both.

const OrderDigits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// ValidOrderKey reports whether k is a well-formed order key.
func ValidOrderKey(k string) bool {
	if k == "" || len(k) > MaxOrderKeyLen || k[len(k)-1] == '0' {
		return false
	}
	for i := 0; i < len(k); i++ {
		if strings.IndexByte(OrderDigits, k[i]) < 0 {
			return false
		}
	}
	return true
}

// OrderBetween returns a key strictly between a and b. Use "" for a to mean
// "before everything" and "" for b to mean "after everything". It panics if
// a >= b (both non-empty) or a key is malformed: callers pass neighbours.
func OrderBetween(a, b string) string {
	if (a != "" && !ValidOrderKey(a)) || (b != "" && !ValidOrderKey(b)) {
		panic("contract.OrderBetween: malformed key")
	}
	if a != "" && b != "" && a >= b {
		panic("contract.OrderBetween: a must sort before b")
	}
	return midpoint(a, b, b == "")
}

// midpoint works on digit strings; open means b is +infinity.
func midpoint(a, b string, open bool) string {
	if !open {
		// Skip the common prefix (treating a as zero-padded).
		n := 0
		for n < len(b) && digitAt(a, n) == b[n] {
			n++
		}
		if n > 0 {
			return b[:n] + midpoint(sliceFrom(a, n), b[n:], false)
		}
	}
	da := 0
	if a != "" {
		da = strings.IndexByte(OrderDigits, a[0])
	}
	db := len(OrderDigits)
	if !open {
		db = strings.IndexByte(OrderDigits, b[0])
	}
	if db-da > 1 {
		return string(OrderDigits[(da+db+1)/2])
	}
	// Adjacent digits: keep a's first digit and recurse into the tail.
	if !open && len(b) > 1 {
		return b[:1]
	}
	return string(OrderDigits[da]) + midpoint(sliceFrom(a, 1), "", true)
}

func digitAt(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return '0'
}

func sliceFrom(s string, i int) string {
	if i >= len(s) {
		return ""
	}
	return s[i:]
}
