package puzzle

import (
	"reflect"
	"testing"
)

func TestParseRange(t *testing.T) {
	for in, want := range map[string][]int{
		"1-5": {1, 2, 3, 4, 5}, "2,4": {2, 4}, "3": {3}, "4,1-2": {1, 2, 4},
	} {
		got, err := ParseRange(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseRange(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "6", "1-6", "x", ""} {
		if _, err := ParseRange(in); err == nil {
			t.Errorf("ParseRange(%q) should fail", in)
		}
	}
}

func TestBoardFromFEN(t *testing.T) {
	b := BoardFromFEN("rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1")
	if len(b) != 32 || b["e4"] != 'P' || b["e8"] != 'k' || b["a1"] != 'R' || b["h7"] != 'p' {
		t.Fatalf("unexpected board: %v", b)
	}
	if _, ok := b["e2"]; ok {
		t.Fatal("e2 should be empty")
	}
}
