package qidecode

import (
	"math/big"
	"testing"
)

func TestDenominationTable(t *testing.T) {
	// Spot-check the consensus table: 0.001 Qi, 1 Qi, 1,000,000 Qi (in qits).
	cases := map[uint8]int64{0: 1, 6: 1000, 14: 1000000000}
	for denom, want := range cases {
		if got := DenominationValue(denom); got.Cmp(big.NewInt(want)) != 0 {
			t.Errorf("denomination %d = %s, want %d", denom, got, want)
		}
	}
}

func TestTrimDeadline(t *testing.T) {
	if d := TrimDeadline(0, 100); d <= 100 {
		t.Errorf("denomination 0 must be trimmable, got deadline %d", d)
	}
	if d := TrimDeadline(6, 100); d != 0 {
		t.Errorf("denomination 6 (1 Qi) must never trim, got deadline %d", d)
	}
}

func TestLockups(t *testing.T) {
	if CoinbaseLockupDepth(0) == 0 {
		t.Error("lockup byte 0 must have a nonzero depth")
	}
	if ConversionLockPeriod() == 0 {
		t.Error("conversion lock period must be nonzero")
	}
}
