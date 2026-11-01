package jsonnumber

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"
)

func TestIntegerRepresentations(t *testing.T) {
	for _, value := range []any{
		int(2), int8(2), int16(2), int32(2), int64(2),
		uint(2), uint8(2), uint16(2), uint32(2), uint64(2),
		float32(2), float64(2), time.Duration(2),
		json.Number("2"), json.Number("2.0"), json.Number("2e0"),
		json.Number("200e-2"), json.Number("0.02e2"),
	} {
		t.Run(fmt.Sprintf("%T/%v", value, value), func(t *testing.T) {
			if got, ok := Int(value); !ok || got != 2 {
				t.Errorf("Int = %d, %v", got, ok)
			}
			if got, ok := Int64(value); !ok || got != 2 {
				t.Errorf("Int64 = %d, %v", got, ok)
			}
			if got, ok := Uint64(value); !ok || got != 2 {
				t.Errorf("Uint64 = %d, %v", got, ok)
			}
		})
	}
}

func TestIntegerRejectsLossyAndInvalidValues(t *testing.T) {
	for _, value := range []any{
		nil, "2", true, float64(2.5), float32(2.5), math.NaN(), math.Inf(1), math.Inf(-1),
		float64(1 << 53), float32(1 << 24),
		json.Number("2.00000000000000000001"), json.Number("9007199254740991.1"),
		json.Number("18446744073709551616"), json.Number("1e1000"), json.Number("1e-1000"),
		json.Number("2e999999999999999999999999999"), json.Number("2e-9223372036854775808"),
		json.Number(""), json.Number("02"), json.Number("NaN"), json.Number("null"), json.Number("true"),
		json.Number(" 2"), json.Number("2 "), json.Number("0e0 "),
	} {
		t.Run(fmt.Sprintf("%T/%v", value, value), func(t *testing.T) {
			if got, ok := Int(value); ok || got != 0 {
				t.Errorf("Int = %d, %v", got, ok)
			}
			if got, ok := Int64(value); ok || got != 0 {
				t.Errorf("Int64 = %d, %v", got, ok)
			}
			if got, ok := Uint64(value); ok || got != 0 {
				t.Errorf("Uint64 = %d, %v", got, ok)
			}
		})
	}
}

func TestIntegerRanges(t *testing.T) {
	for _, tc := range []struct {
		value any
		i     int64
		iOK   bool
		u     uint64
		uOK   bool
	}{
		{json.Number("9007199254740993"), 9007199254740993, true, 9007199254740993, true},
		{json.Number("9007199254740993.0"), 9007199254740993, true, 9007199254740993, true},
		{json.Number("9.007199254740993e15"), 9007199254740993, true, 9007199254740993, true},
		{json.Number("-9223372036854775808.0"), math.MinInt64, true, 0, false},
		{int64(math.MinInt64), math.MinInt64, true, 0, false},
		{json.Number("9223372036854775807e0"), math.MaxInt64, true, math.MaxInt64, true},
		{json.Number("9223372036854775808"), 0, false, 1 << 63, true},
		{json.Number("18446744073709551615.0"), 0, false, math.MaxUint64, true},
		{uint64(math.MaxUint64), 0, false, math.MaxUint64, true},
		{json.Number("-18446744073709551615"), 0, false, 0, false},
		{json.Number("-0e99999999999999999999999"), 0, true, 0, true},
		{json.Number("0e-99999999999999999999999"), 0, true, 0, true},
		{json.Number("-2.0"), -2, true, 0, false},
	} {
		t.Run(fmt.Sprintf("%T/%v", tc.value, tc.value), func(t *testing.T) {
			if got, ok := Int64(tc.value); got != tc.i || ok != tc.iOK {
				t.Errorf("Int64 = %d, %v; want %d, %v", got, ok, tc.i, tc.iOK)
			}
			if got, ok := Uint64(tc.value); got != tc.u || ok != tc.uOK {
				t.Errorf("Uint64 = %d, %v; want %d, %v", got, ok, tc.u, tc.uOK)
			}
			want, valid := int(tc.i), tc.iOK && (strconv.IntSize == 64 || int64(int(tc.i)) == tc.i)
			if !valid {
				want = 0
			}
			if got, ok := Int(tc.value); got != want || ok != valid {
				t.Errorf("Int = %d, %v; want %d, %v", got, ok, want, valid)
			}
		})
	}
}
