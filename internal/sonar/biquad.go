package sonar

import "math"

// Sonar's EQ bands run as PipeWire filter-chain bq_raw filters whose
// coefficients ggd computes, so a band's type, frequency, gain and Q can all
// change while audio plays. The formulas are the Audio EQ Cookbook's (RBJ),
// normalized so a0 = 1.

// sampleRate is the rate Sonar's filter chains run at.
const sampleRate = 48000

type biquad struct{ b0, b1, b2, a0, a1, a2 float64 }

var identity = biquad{b0: 1, a0: 1}

// newBiquad designs one of Sonar's filter types ("peakingEQ", "lowShelving",
// "highShelving", "lowPass", "highPass", "notchFilter"); anything else, or
// out-of-range parameters, gives a pass-through filter.
func newBiquad(typ string, freq, gainDB, q float64) biquad {
	if freq <= 0 || freq >= sampleRate/2 || q <= 0 {
		return identity
	}
	w0 := 2 * math.Pi * freq / sampleRate
	cos, alpha := math.Cos(w0), math.Sin(w0)/(2*q)
	a := math.Pow(10, gainDB/40)
	var f biquad
	switch typ {
	case "peakingEQ":
		f = biquad{1 + alpha*a, -2 * cos, 1 - alpha*a, 1 + alpha/a, -2 * cos, 1 - alpha/a}
	case "lowShelving":
		sq := 2 * math.Sqrt(a) * alpha
		f = biquad{
			a * ((a + 1) - (a-1)*cos + sq), 2 * a * ((a - 1) - (a+1)*cos), a * ((a + 1) - (a-1)*cos - sq),
			(a + 1) + (a-1)*cos + sq, -2 * ((a - 1) + (a+1)*cos), (a + 1) + (a-1)*cos - sq,
		}
	case "highShelving":
		sq := 2 * math.Sqrt(a) * alpha
		f = biquad{
			a * ((a + 1) + (a-1)*cos + sq), -2 * a * ((a - 1) + (a+1)*cos), a * ((a + 1) + (a-1)*cos - sq),
			(a + 1) - (a-1)*cos + sq, 2 * ((a - 1) - (a+1)*cos), (a + 1) - (a-1)*cos - sq,
		}
	case "lowPass":
		f = biquad{(1 - cos) / 2, 1 - cos, (1 - cos) / 2, 1 + alpha, -2 * cos, 1 - alpha}
	case "highPass":
		f = biquad{(1 + cos) / 2, -(1 + cos), (1 + cos) / 2, 1 + alpha, -2 * cos, 1 - alpha}
	case "notchFilter":
		f = biquad{1, -2 * cos, 1, 1 + alpha, -2 * cos, 1 - alpha}
	default:
		return identity
	}
	return biquad{f.b0 / f.a0, f.b1 / f.a0, f.b2 / f.a0, 1, f.a1 / f.a0, f.a2 / f.a0}
}

// response is the filter's gain in dB at freq.
func (f biquad) response(freq float64) float64 {
	w := 2 * math.Pi * freq / sampleRate
	z1 := complex(math.Cos(-w), math.Sin(-w))
	z2 := z1 * z1
	h := (complex(f.b0, 0) + complex(f.b1, 0)*z1 + complex(f.b2, 0)*z2) /
		(complex(f.a0, 0) + complex(f.a1, 0)*z1 + complex(f.a2, 0)*z2)
	mag := math.Hypot(real(h), imag(h))
	return 20 * math.Log10(mag)
}
