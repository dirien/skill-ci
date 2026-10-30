package stats

import (
	"math"
	"testing"
)

func near(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

func TestMeanVarianceStdErr(t *testing.T) {
	tests := []struct {
		name               string
		x                  []float64
		mean, variance, se float64
	}{
		{"empty", nil, 0, 0, 0},
		{"one value has no spread", []float64{0.9}, 0.9, 0, 0},
		{"constant", []float64{0.95, 0.95, 0.95}, 0.95, 0, 0},
		// 2,4,4,4,5,5,7,9: mean 5, sum of squares 32, variance 32/7.
		{"textbook", []float64{2, 4, 4, 4, 5, 5, 7, 9}, 5, 32.0 / 7, math.Sqrt(32.0 / 7 / 8)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Mean(tt.x); !near(got, tt.mean, 1e-12) {
				t.Errorf("Mean() = %v, want %v", got, tt.mean)
			}
			if got := Variance(tt.x); !near(got, tt.variance, 1e-12) {
				t.Errorf("Variance() = %v, want %v", got, tt.variance)
			}
			if got := StdErr(tt.x); !near(got, tt.se, 1e-12) {
				t.Errorf("StdErr() = %v, want %v", got, tt.se)
			}
		})
	}
}

// Known two-sided critical values from standard t tables.
func TestTQuantile(t *testing.T) {
	tests := []struct {
		p, df, want float64
	}{
		{0.975, 1, 12.7062},
		{0.975, 2, 4.3027},
		{0.975, 4, 2.7764},
		{0.975, 8, 2.3060},
		{0.975, 10, 2.2281},
		{0.975, 16, 2.1199},
		{0.975, 30, 2.0423},
		{0.975, 120, 1.9799},
		{0.975, 1e6, 1.9600},
		{0.95, 5, 2.0150},
		{0.995, 10, 3.1693},
		{0.9, 3, 1.6377},
		{0.025, 8, -2.3060},
		{0.5, 8, 0},
	}
	for _, tt := range tests {
		if got := TQuantile(tt.p, tt.df); !near(got, tt.want, 5e-4) {
			t.Errorf("TQuantile(%v, %v) = %.4f, want %.4f", tt.p, tt.df, got, tt.want)
		}
	}
	for _, bad := range [][2]float64{{0, 5}, {1, 5}, {0.9, 0}, {math.NaN(), 5}} {
		if got := TQuantile(bad[0], bad[1]); !math.IsNaN(got) {
			t.Errorf("TQuantile(%v, %v) = %v, want NaN", bad[0], bad[1], got)
		}
	}
}

func TestWelchDiff(t *testing.T) {
	// Reference values computed with scipy:
	//   a=[0.95,0.9,0.85,0.95,0.95,0.8,0.9,0.95,0.85], b=[0.36,0.38,0.35,0.3,0.4,0.35,0.36,0.37,0.38]
	//   scipy.stats.ttest_ind(a, b, equal_var=False).confidence_interval(conf)
	a := []float64{0.95, 0.9, 0.85, 0.95, 0.95, 0.8, 0.9, 0.95, 0.85}
	b := []float64{0.36, 0.38, 0.35, 0.3, 0.4, 0.35, 0.36, 0.37, 0.38}
	tests := []struct {
		name               string
		a, b               []float64
		conf               float64
		ok                 bool
		diff, lower, upper float64
	}{
		{"clear lift", a, b, 0.95, true, 0.538889, 0.493377, 0.584401},
		{"wider at 99 percent", a, b, 0.99, true, 0.538889, 0.475001, 0.602777},
		{"identical samples cross zero", []float64{0.8, 0.9, 0.7}, []float64{0.8, 0.9, 0.7}, 0.95, true, 0, -0.226696, 0.226696},
		{"no spread collapses", []float64{0.95, 0.95, 0.95}, []float64{0.36, 0.36, 0.36}, 0.95, true, 0.59, 0.59, 0.59},
		{"one value is not enough", []float64{0.9}, b, 0.95, false, 0, 0, 0},
		{"bad confidence", a, b, 1, false, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := WelchDiff(tt.a, tt.b, tt.conf)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if !near(got.Diff, tt.diff, 1e-5) || !near(got.Lower, tt.lower, 1e-5) || !near(got.Upper, tt.upper, 1e-5) {
				t.Errorf("WelchDiff() = %+v, want diff %.4f CI [%.4f, %.4f]", got, tt.diff, tt.lower, tt.upper)
			}
		})
	}
}
