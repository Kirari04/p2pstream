package tunnel

import "testing"

func TestSocketOverrideAndAutoInitialStreamCharges(t *testing.T) {
	for _, test := range []struct{ buffer, want int64 }{
		{0, DefaultAdaptiveStreamChargeBytes},
		{16 << 10, MinimumAdaptiveStreamChargeBytes},
		{128 << 10, DefaultAdaptiveStreamChargeBytes},
		{16 << 20, (512 << 10) + (64 << 20) + (256 << 10)},
	} {
		if normalized, err := NormalizeUpstreamSocketBufferBytes(test.buffer); err != nil || normalized != test.buffer {
			t.Fatalf("socket override %d changed to %d: %v", test.buffer, normalized, err)
		}
		if charge, err := StreamMemoryCharge(0, test.buffer); err != nil || charge != test.want {
			t.Fatalf("buffer %d initial charge = %d, want %d: %v", test.buffer, charge, test.want, err)
		}
	}
}
