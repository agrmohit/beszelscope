package report

import (
	"math"
	"strconv"
	"time"
)

// round returns v rounded to d decimals, or nil if v is NaN or infinite,
// which JSON cannot encode.
func round(v float64, d int) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	r := roundTo(v, d)
	return &r
}

// roundTo rounds v to d decimals, for values that are finite by
// construction (hub JSON floats and guarded ratios).
func roundTo(v float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.Round(v*p) / p
}

// ageS is whole seconds from t to now; never negative.
func ageS(now, t time.Time) int64 {
	return max(0, int64(now.Sub(t)/time.Second))
}

// short formats a duration in up to two units: "45s", "7m", "1h 55m", "3h",
// "2d 6h" or "2d". One floored unit read 1h55m as "1h".
func short(d time.Duration) string {
	two := func(big, small time.Duration, bigUnit, smallUnit string) string {
		s := strconv.FormatInt(int64(d/big), 10) + bigUnit
		if rest := d % big / small; rest > 0 {
			s += " " + strconv.FormatInt(int64(rest), 10) + smallUnit
		}
		return s
	}
	switch {
	case d < time.Minute:
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	case d < time.Hour:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	case d < 24*time.Hour:
		return two(time.Hour, time.Minute, "h", "m")
	default:
		return two(24*time.Hour, time.Hour, "d", "h")
	}
}
