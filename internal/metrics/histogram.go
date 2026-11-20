package metrics

// latencyBounds are the latency buckets' upper bounds in milliseconds; a sample
// goes to the first bucket whose bound is >= it, a sample above the last bound
// to the overflow bucket. Fixed, so an entry's memory does not grow with traffic.
var latencyBounds = [...]int64{0, 1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 30000, 60000, 120000}

// histogram counts latency samples (integer ms) into latencyBounds plus an
// overflow bucket and keeps the exact maximum. Not safe for concurrent use: the
// Collector's mutex guards it.
type histogram struct {
	counts [len(latencyBounds) + 1]uint64
	n      uint64
	max    int64
}

func (h *histogram) add(ms int64) {
	if ms < 0 {
		ms = 0
	}
	i := len(latencyBounds) // overflow
	for j, b := range latencyBounds {
		if ms <= b {
			i = j
			break
		}
	}
	h.counts[i]++
	h.n++
	if ms > h.max {
		h.max = ms
	}
}

// percentile returns the p-th percentile (p in percent, 1..100): the upper bound
// of the bucket holding rank (n*p+99)/100, capped by max; the overflow bucket
// reports max; an empty histogram reports 0. The rank uses integer math so no
// float rounding moves it.
func (h *histogram) percentile(p uint64) int64 {
	if h.n == 0 {
		return 0
	}
	rank := (h.n*p + 99) / 100
	if rank < 1 {
		rank = 1
	}
	var seen uint64
	for i, c := range h.counts {
		seen += c
		if seen < rank {
			continue
		}
		if i == len(latencyBounds) {
			return h.max
		}
		return min(latencyBounds[i], h.max)
	}
	return h.max
}

// Latency is a histogram's summary as the endpoint reports it.
type Latency struct {
	Count uint64 `json:"count"`
	P50   int64  `json:"p50"`
	P95   int64  `json:"p95"`
	P99   int64  `json:"p99"`
	Max   int64  `json:"max"`
}

func (h *histogram) summary() Latency {
	return Latency{Count: h.n, P50: h.percentile(50), P95: h.percentile(95), P99: h.percentile(99), Max: h.max}
}
