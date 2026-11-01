// Package jsonnumber reads integer-valued JSON payloads without rounding.
package jsonnumber

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// Int64 accepts Go integers (including live Notice time.Duration values), exact
// JSON integers, and integral floats within their consecutive-integer range.
// Decimal/exponent JSON forms are evaluated exactly; strings are not numbers.
func Int64(value any) (int64, bool) {
	negative, magnitude, ok := integer(value)
	if !ok || (!negative && magnitude > math.MaxInt64) || (negative && magnitude > uint64(1)<<63) {
		return 0, false
	}
	if negative {
		return -int64(magnitude), true
	}
	return int64(magnitude), true
}

// Int is Int64 restricted to the platform's int range.
func Int(value any) (int, bool) {
	n, ok := Int64(value)
	if !ok || int64(int(n)) != n {
		return 0, false
	}
	return int(n), true
}

// Uint64 accepts the same representations as Int64, restricted to nonnegative
// values in the uint64 range. It never wraps a negative or oversized value.
func Uint64(value any) (uint64, bool) {
	negative, magnitude, ok := integer(value)
	if !ok || negative && magnitude != 0 {
		return 0, false
	}
	return magnitude, true
}

func integer(value any) (negative bool, magnitude uint64, ok bool) {
	var signed int64
	switch n := value.(type) {
	case int:
		signed = int64(n)
	case int8:
		signed = int64(n)
	case int16:
		signed = int64(n)
	case int32:
		signed = int64(n)
	case int64:
		signed = n
	case time.Duration:
		signed = int64(n)
	case uint:
		return false, uint64(n), true
	case uint8:
		return false, uint64(n), true
	case uint16:
		return false, uint64(n), true
	case uint32:
		return false, uint64(n), true
	case uint64:
		return false, n, true
	case float32:
		if math.Abs(float64(n)) > 1<<24-1 {
			return false, 0, false
		}
		return integer(float64(n))
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || math.Abs(n) > 1<<53-1 {
			return false, 0, false
		}
		signed = int64(n)
	case json.Number:
		return jsonInteger(n)
	default:
		return false, 0, false
	}
	if signed < 0 {
		return true, uint64(-(signed + 1)) + 1, true
	}
	return false, uint64(signed), true
}

func jsonInteger(n json.Number) (negative bool, magnitude uint64, ok bool) {
	raw := string(n)
	if len(raw) == 0 || strings.TrimSpace(raw) != raw || !json.Valid([]byte(raw)) {
		return false, 0, false
	}
	negative = raw[0] == '-'
	if negative {
		raw = raw[1:]
	}
	if raw[0] < '0' || raw[0] > '9' {
		return false, 0, false
	}
	mantissa, exponent, hasExponent := strings.Cut(strings.ToLower(raw), "e")
	whole, fraction, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return negative, 0, true
	}
	var e int64
	if hasExponent {
		var err error
		e, err = strconv.ParseInt(exponent, 10, 64)
		// Bound expansion before allocating. No nonzero uint64-sized integer
		// can need more than 20 digits, even with cancellable trailing zeros.
		if err != nil || e > int64(len(mantissa))+20 || e < -int64(len(mantissa)) {
			return false, 0, false
		}
	}
	scale := e - int64(len(fraction))
	if scale < 0 {
		cut := int64(len(digits)) + scale
		if cut <= 0 || strings.Trim(digits[cut:], "0") != "" {
			return false, 0, false
		}
		digits = digits[:cut]
	} else if scale > 0 {
		if int64(len(digits))+scale > 20 {
			return false, 0, false
		}
		digits += strings.Repeat("0", int(scale))
	}
	magnitude, err := strconv.ParseUint(digits, 10, 64)
	return negative, magnitude, err == nil
}
