package feed

import (
	"math"
	"testing"
)

func TestPopularityTimeDecayWeights(t *testing.T) {
	tests := []struct {
		age  int
		want float64
	}{
		{age: 0, want: 1},
		{age: 30, want: 0.5},
		{age: 60, want: 0.25},
	}
	for _, tt := range tests {
		got := popularityTimeWeight(tt.age)
		if math.Abs(got-tt.want) > 0.000001 {
			t.Fatalf("weight at %d minutes = %f, want %f", tt.age, got, tt.want)
		}
	}
}
