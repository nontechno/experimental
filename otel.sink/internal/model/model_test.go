package model

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// A histogram's overflow bucket has an upper bound of +Inf, which plain
// encoding/json refuses to marshal. That used to fail the whole response.
func TestHistogramWithInfiniteBucketEncodes(t *testing.T) {
	m := Metric{
		Name: "checkout.duration",
		Type: "Histogram",
		DataPoints: []DataPoint{{
			Count: 3,
			Sum:   42,
			Buckets: []Bucket{
				{UpperBound: Float(10), Count: 1},
				{UpperBound: Float(math.Inf(1)), Count: 2},
			},
		}},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"upper_bound":null`) {
		t.Fatalf("+Inf bound should encode as null, got %s", b)
	}
}

func TestFloatRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   Float
		want string
	}{
		{"finite", Float(1.5), "1.5"},
		{"zero", Float(0), "0"},
		{"positive infinity", Float(math.Inf(1)), "null"},
		{"negative infinity", Float(math.Inf(-1)), "null"},
		{"not a number", Float(math.NaN()), "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(b) != tc.want {
				t.Fatalf("got %s, want %s", b, tc.want)
			}
			var back Float
			if err := json.Unmarshal(b, &back); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
		})
	}
}

// A gauge reporting NaN is unusual but legal, and must not break the batch.
func TestNaNDataPointEncodes(t *testing.T) {
	b, err := json.Marshal(DataPoint{Value: Float(math.NaN())})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"value":null`) {
		t.Fatalf("NaN value should encode as null, got %s", b)
	}
}
