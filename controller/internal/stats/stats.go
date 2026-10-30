// Package stats holds the few statistics the gate needs: mean, standard error
// and Welch's t confidence interval for a difference of means. It is pure and
// has no dependencies beyond the standard library.
package stats

import "math"

// Mean returns the arithmetic mean of x, or 0 for an empty slice.
func Mean(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range x {
		sum += v
	}
	return sum / float64(len(x))
}

// Variance returns the sample variance of x (divisor n-1), or 0 when x has
// fewer than two values.
func Variance(x []float64) float64 {
	if len(x) < 2 {
		return 0
	}
	m, ss := Mean(x), 0.0
	for _, v := range x {
		ss += (v - m) * (v - m)
	}
	return ss / float64(len(x)-1)
}

// StdErr returns the standard error of the mean of x: the sample standard
// deviation over the square root of n. It is 0 when x has fewer than two values.
func StdErr(x []float64) float64 {
	if len(x) < 2 {
		return 0
	}
	return math.Sqrt(Variance(x) / float64(len(x)))
}

// Interval is a confidence interval for a difference of means.
type Interval struct {
	// Diff is the difference of the two means, a minus b.
	Diff float64
	// Lower and Upper are the bounds of the interval around Diff.
	Lower, Upper float64
}

// WelchDiff returns the confidence interval for mean(a) - mean(b) at the given
// confidence level (for example 0.95), using Welch's t with the
// Welch-Satterthwaite degrees of freedom. It reports false when either sample
// has fewer than two values or the confidence is not in (0, 1). When both
// samples have no spread the interval collapses to the difference.
func WelchDiff(a, b []float64, confidence float64) (Interval, bool) {
	if len(a) < 2 || len(b) < 2 || confidence <= 0 || confidence >= 1 {
		return Interval{}, false
	}
	diff := Mean(a) - Mean(b)
	va, vb := Variance(a)/float64(len(a)), Variance(b)/float64(len(b))
	se2 := va + vb
	if se2 == 0 {
		return Interval{Diff: diff, Lower: diff, Upper: diff}, true
	}
	df := se2 * se2 / (va*va/float64(len(a)-1) + vb*vb/float64(len(b)-1))
	margin := TQuantile(1-(1-confidence)/2, df) * math.Sqrt(se2)
	return Interval{Diff: diff, Lower: diff - margin, Upper: diff + margin}, true
}

// TQuantile returns the p-quantile (0 < p < 1) of Student's t distribution
// with df degrees of freedom (df may be fractional). It inverts the
// distribution function by bisection, which is plenty fast for a handful of calls.
func TQuantile(p, df float64) float64 {
	switch {
	case p <= 0 || p >= 1 || df <= 0 || math.IsNaN(p) || math.IsNaN(df):
		return math.NaN()
	case p == 0.5:
		return 0
	case p < 0.5:
		return -TQuantile(1-p, df)
	}
	lo, hi := 0.0, 1.0
	for tCDF(hi, df) < p && hi < 1e12 {
		lo, hi = hi, hi*2
	}
	for range 200 {
		mid := (lo + hi) / 2
		if tCDF(mid, df) < p {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// tCDF is the distribution function of Student's t for t >= 0:
// P(T <= t) = 1 - I_x(df/2, 1/2) / 2 with x = df / (df + t^2).
func tCDF(t, df float64) float64 {
	return 1 - betaInc(df/2, 0.5, df/(df+t*t))/2
}

// betaInc is the regularized incomplete beta function I_x(a, b), by the
// continued fraction of Numerical Recipes (betacf).
func betaInc(a, b, x float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	la, _ := math.Lgamma(a)
	lb, _ := math.Lgamma(b)
	lab, _ := math.Lgamma(a + b)
	front := math.Exp(lab - la - lb + a*math.Log(x) + b*math.Log(1-x))
	if x < (a+1)/(a+b+2) {
		return front * betaCF(a, b, x) / a
	}
	return 1 - front*betaCF(b, a, 1-x)/b
}

// betaCF evaluates the continued fraction of the incomplete beta function with
// the modified Lentz method.
func betaCF(a, b, x float64) float64 {
	const tiny, eps = 1e-300, 1e-14
	c, d := 1.0, 1-(a+b)*x/(a+1)
	if math.Abs(d) < tiny {
		d = tiny
	}
	d = 1 / d
	h := d
	for m := 1; m <= 500; m++ {
		fm := float64(m)
		num := fm * (b - fm) * x / ((a + 2*fm - 1) * (a + 2*fm))
		d, c = 1+num*d, 1+num/c
		d, c = nonZero(d, tiny), nonZero(c, tiny)
		d = 1 / d
		h *= d * c
		num = -(a + fm) * (a + b + fm) * x / ((a + 2*fm) * (a + 2*fm + 1))
		d, c = 1+num*d, 1+num/c
		d, c = nonZero(d, tiny), nonZero(c, tiny)
		d = 1 / d
		delta := d * c
		h *= delta
		if math.Abs(delta-1) < eps {
			break
		}
	}
	return h
}

func nonZero(v, tiny float64) float64 {
	if math.Abs(v) < tiny {
		return tiny
	}
	return v
}
