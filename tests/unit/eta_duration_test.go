package unit_test

import (
	"math"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/telemetry"
)

func TestDurationForSecondsRejectsUnrepresentableETA(t *testing.T) {
	for _, seconds := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1), float64(math.MaxInt64)} {
		if duration, valid := telemetry.DurationForSeconds(seconds); valid || duration < 0 {
			t.Fatalf("accepted invalid ETA %v as %s", seconds, duration)
		}
	}
	for _, test := range []struct {
		seconds float64
		want    time.Duration
	}{
		{seconds: 0, want: 0},
		{seconds: 1.5, want: 1500 * time.Millisecond},
		{seconds: 60, want: time.Minute},
	} {
		duration, valid := telemetry.DurationForSeconds(test.seconds)
		if !valid || duration != test.want {
			t.Fatalf("ETA %v became %s, valid=%t", test.seconds, duration, valid)
		}
	}
}
