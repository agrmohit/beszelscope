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
	p := math.Pow(10, float64(d))
	r := math.Round(v*p) / p
	return &r
}

// ageS is whole seconds from t to now; never negative.
func ageS(now, t time.Time) int64 {
	return max(0, int64(now.Sub(t)/time.Second))
}

// short formats a duration as "45s", "7m", "3h" or "2d".
func short(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	case d < time.Hour:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	case d < 48*time.Hour:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	default:
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	}
}
