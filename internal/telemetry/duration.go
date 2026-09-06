package telemetry

import (
	"math"
	"time"
)

func DurationForSeconds(seconds float64) (time.Duration, bool) {
	maximum := float64(math.MaxInt64 / int64(time.Second))
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > maximum {
		return 0, false
	}
	duration := time.Duration(seconds * float64(time.Second))
	if duration < 0 {
		return 0, false
	}

	return duration, true
}
