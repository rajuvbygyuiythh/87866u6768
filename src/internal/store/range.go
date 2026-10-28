package store

// Min/max random timing ranges: each chat-control setting can be a RANGE
// (key_min .. key_max) and every use draws a fresh random value inside it —
// humans never wait exactly the same time twice.
//
// Backward compatible: a legacy single key (stranger_response_time=2) still
// works when no _min/_max keys exist (min == max == legacy).

import "math/rand"

// RangeSecs resolves the (min, max) seconds for a timing setting.
func (s *Store) RangeSecs(key string, defMin, defMax float64) (float64, float64) {
	min := s.Float(key+"_min", 0)
	max := s.Float(key+"_max", 0)
	if min <= 0 && max <= 0 {
		if legacy := s.Float(key, 0); legacy > 0 {
			return legacy, legacy // old single-value setting = fixed time
		}
		return defMin, defMax
	}
	if min <= 0 {
		min = max
	}
	if max <= 0 {
		max = min
	}
	if min > max {
		min, max = max, min
	}
	return min, max
}

// RandBetween draws a uniform random value in [min, max].
func RandBetween(min, max float64) float64 {
	if max <= min {
		return min
	}
	return min + rand.Float64()*(max-min)
}
