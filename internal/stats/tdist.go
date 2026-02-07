package stats

import "math"

// tDistSurvival returns P(T > t) for a t-distribution with df degrees of freedom.
// Uses the regularized incomplete beta function for accuracy across all df values.
func tDistSurvival(t float64, df int) float64 {
	if df < 1 {
		df = 1
	}
	if t == 0 {
		return 0.5
	}
	if t < 0 {
		// Symmetry: S(-t) = 1 - S(t)
		return 1 - tDistSurvival(-t, df)
	}

	// For very large df, normal approximation is numerically stable and accurate.
	if df > 1000 {
		return 0.5 * math.Erfc(t/math.Sqrt2)
	}

	x := float64(df) / (float64(df) + t*t)
	return 0.5 * regIncBeta(x, float64(df)/2.0, 0.5)
}

// regIncBeta computes the regularized incomplete beta function I_x(a, b)
// using the continued fraction expansion (Lentz's algorithm).
func regIncBeta(x, a, b float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}

	lgAB, _ := math.Lgamma(a + b)
	lgA, _ := math.Lgamma(a)
	lgB, _ := math.Lgamma(b)
	logBeta := lgAB - lgA - lgB
	bt := math.Exp(logBeta + a*math.Log(x) + b*math.Log(1-x))

	result := 0.0
	if x < (a+1)/(a+b+2) {
		result = bt * betaContinuedFraction(a, b, x) / a
	} else {
		result = 1 - bt*betaContinuedFraction(b, a, 1-x)/b
	}

	if result < 0 {
		return 0
	}
	if result > 1 {
		return 1
	}
	return result
}

func betaContinuedFraction(a, b, x float64) float64 {
	const maxIterations = 200
	const epsilon = 3e-14
	const fpMin = 1e-300

	qab := a + b
	qap := a + 1
	qam := a - 1

	c := 1.0
	d := 1.0 - qab*x/qap
	if math.Abs(d) < fpMin {
		d = fpMin
	}
	d = 1 / d
	h := d

	for m := 1; m <= maxIterations; m++ {
		mFloat := float64(m)
		m2 := float64(2 * m)

		aa := mFloat * (b - mFloat) * x / ((qam + m2) * (a + m2))
		d = 1 + aa*d
		if math.Abs(d) < fpMin {
			d = fpMin
		}
		c = 1 + aa/c
		if math.Abs(c) < fpMin {
			c = fpMin
		}
		d = 1 / d
		h *= d * c

		aa = -(a + mFloat) * (qab + mFloat) * x / ((a + m2) * (qap + m2))
		d = 1 + aa*d
		if math.Abs(d) < fpMin {
			d = fpMin
		}
		c = 1 + aa/c
		if math.Abs(c) < fpMin {
			c = fpMin
		}
		d = 1 / d
		delta := d * c
		h *= delta

		if math.Abs(delta-1) < epsilon {
			break
		}
	}

	return h
}
