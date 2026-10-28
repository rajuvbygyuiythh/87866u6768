package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRangeSecs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	_ = os.MkdirAll(dir, 0o755)

	// defaults when nothing is set
	st := &Store{Dir: dir}
	mn, mx := st.RangeSecs("stranger_response_time", 2, 5)
	if mn != 2 || mx != 5 {
		t.Fatalf("defaults wrong: %v..%v", mn, mx)
	}

	// legacy single value becomes a fixed range
	st.SaveSettings(map[string]string{"stranger_response_time": "3"})
	mn, mx = st.RangeSecs("stranger_response_time", 2, 5)
	if mn != 3 || mx != 3 {
		t.Fatalf("legacy fallback wrong: %v..%v", mn, mx)
	}

	// min/max keys win over legacy; swapped values get normalized
	st.SaveSettings(map[string]string{"stranger_response_time_min": "8", "stranger_response_time_max": "4"})
	mn, mx = st.RangeSecs("stranger_response_time", 2, 5)
	if mn != 4 || mx != 8 {
		t.Fatalf("min/max resolution wrong: %v..%v", mn, mx)
	}
}

func TestRandBetweenBounds(t *testing.T) {
	for i := 0; i < 1000; i++ {
		v := RandBetween(3, 8)
		if v < 3 || v > 8 {
			t.Fatalf("out of range: %v", v)
		}
	}
	if RandBetween(5, 5) != 5 {
		t.Fatal("fixed range must return the value")
	}
	if RandBetween(9, 2) != 2 && RandBetween(9, 2) != 9 {
		// min>max is normalized by callers, but RandBetween must not panic
		t.Fatal("degenerate range handling")
	}
}
