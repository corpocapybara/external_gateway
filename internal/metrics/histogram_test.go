package metrics

import "testing"

func TestHistogramPercentiles(t *testing.T) {
	seq := func(from, to int64) []int64 {
		var s []int64
		for v := from; v <= to; v++ {
			s = append(s, v)
		}
		return s
	}
	cases := []struct {
		name    string
		samples []int64
		want    Latency
	}{
		{"empty", nil, Latency{}},
		{"one 7 ms", []int64{7}, Latency{Count: 1, P50: 7, P95: 7, P99: 7, Max: 7}},
		{"on a bound", []int64{5}, Latency{Count: 1, P50: 5, P95: 5, P99: 5, Max: 5}},
		{"zero", []int64{0}, Latency{Count: 1}},
		{"overflow", []int64{200000}, Latency{Count: 1, P50: 200000, P95: 200000, P99: 200000, Max: 200000}},
		{"negative", []int64{-3}, Latency{Count: 1}},
		{"1..100", seq(1, 100), Latency{Count: 100, P50: 50, P95: 100, P99: 100, Max: 100}},
		{"capped by max", []int64{3, 3, 3}, Latency{Count: 3, P50: 3, P95: 3, P99: 3, Max: 3}},
		// 98 fast and 2 slow: p95 stays in the fast bucket, p99 reaches the slow one.
		{"tail", append(append([]int64{}, make([]int64, 98)...), 700, 900),
			Latency{Count: 100, P50: 0, P95: 0, P99: 900, Max: 900}},
	}
	for _, c := range cases {
		var h histogram
		for _, v := range c.samples {
			h.add(v)
		}
		if got := h.summary(); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
