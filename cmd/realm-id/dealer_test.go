package main

// The CLI keeps its own dealer (console-tier client, owner 2026-11-02) and is held to the shared
// SPEC §14.14 vectors by the four SPEC14_14_dealer checks; mirrors sdk/go/dealer_test.go. Check 4
// (shape) lives in TestSigningKeysInit_FileShareIsASoundSplitAndTheOutputsAreRight.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"
)

const splitVectorsPath = "testdata/split-vectors.json"

type dealerVec struct {
	Key struct {
		N, D, P, Q string
	} `json:"key"`
	Dealer struct {
		DRI      string `json:"d_ri"`
		DPartner string `json:"d_partner"`
	} `json:"dealer"`
}

func hexBig(t *testing.T, s string) *big.Int {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil || len(b) == 0 {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return new(big.Int).SetBytes(b)
}

// TestSplitVectorsMatchTheirPin: the vendored vectors must be the bytes SPLIT_VECTORS_CONTRACT
// names (scripts/revendor-split-vectors.sh).
func TestSplitVectorsMatchTheirPin(t *testing.T) {
	raw, err := os.ReadFile("SPLIT_VECTORS_CONTRACT")
	if err != nil {
		t.Fatalf("SPLIT_VECTORS_CONTRACT unreadable (%v); re-vendor with scripts/revendor-split-vectors.sh <sdk sha>", err)
	}
	meta := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("SPLIT_VECTORS_CONTRACT: unparseable line %q", line)
		}
		meta[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if len(meta["sdk_sha"]) != 40 {
		t.Fatalf("sdk_sha %q is not a 40-hex commit", meta["sdk_sha"])
	}
	body, err := os.ReadFile(splitVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != meta["sha256"] {
		t.Fatalf("vendored split-vectors.json sha256 %s != pinned %s: hand-edit, or a re-vendor that missed the contract", got, meta["sha256"])
	}
}

// SPEC14_14_dealer check 1: the vector split under lambda(n); d_ri > d, so phi would differ.
func TestDealer_VectorSplitIsLambdaNotPhi(t *testing.T) {
	raw, err := os.ReadFile(splitVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var v dealerVec
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.Dealer.DRI == "" {
		t.Fatal("split-vectors.json has no dealer case")
	}
	p, q, d := hexBig(t, v.Key.P), hexBig(t, v.Key.Q), hexBig(t, v.Key.D)
	dRI := hexBig(t, v.Dealer.DRI)
	if dRI.Cmp(d) <= 0 {
		t.Fatal("dealer case must have d_ri > d")
	}
	dP, err := splitWithDRI(p, q, d, dRI)
	if err != nil {
		t.Fatal(err)
	}
	if dP.Cmp(hexBig(t, v.Dealer.DPartner)) != 0 {
		t.Fatalf("d_partner = %x, want the vector's", dP)
	}
}

// SPEC14_14_dealer checks 2 and 3 over 20 fresh deals, through the dealer init/rotate call.
func TestDealer_RangeAndRoundTripOver20Deals(t *testing.T) {
	one := big.NewInt(1)
	for i := range 20 {
		k, err := dealKey()
		if err != nil {
			t.Fatal(err)
		}
		if new(big.Int).Mul(k.p, k.q).Cmp(k.n) != 0 {
			t.Fatalf("deal %d: p*q != n", i)
		}
		p1 := new(big.Int).Sub(k.p, one)
		q1 := new(big.Int).Sub(k.q, one)
		lam := new(big.Int).Div(new(big.Int).Mul(p1, q1), new(big.Int).GCD(nil, nil, p1, q1))
		for name, x := range map[string]*big.Int{"d_ri": k.dRI, "d_partner": k.dPartner} {
			if x.Cmp(one) < 0 || x.Cmp(lam) >= 0 {
				t.Fatalf("deal %d: %s out of [1, lambda)", i, name)
			}
		}
		digest := sha256.Sum256([]byte("round trip message"))
		m, err := emsaInt(digest[:], (k.n.BitLen()+7)/8)
		if err != nil {
			t.Fatal(err)
		}
		s := new(big.Int).Mul(new(big.Int).Exp(m, k.dRI, k.n), new(big.Int).Exp(m, k.dPartner, k.n))
		s.Mod(s, k.n)
		d := new(big.Int).ModInverse(k.e, lam)
		if s.Cmp(new(big.Int).Exp(m, d, k.n)) != 0 {
			t.Fatalf("deal %d: combined != whole-key signature", i)
		}
		if new(big.Int).Exp(s, k.e, k.n).Cmp(m) != 0 {
			t.Fatalf("deal %d: combined signature does not verify under (n, e)", i)
		}
	}
}

func TestDealer_ZeroHalfIsRefused(t *testing.T) {
	p, q := big.NewInt(61), big.NewInt(53)
	lam := big.NewInt(780) // lcm(60, 52)
	d := big.NewInt(413)   // 17^-1 mod 780
	if _, err := splitWithDRI(p, q, d, big.NewInt(0)); err == nil {
		t.Error("d_ri = 0 accepted")
	}
	if _, err := splitWithDRI(p, q, d, d); err == nil {
		t.Error("d_partner = 0 accepted")
	}
	if _, err := splitWithDRI(p, q, d, lam); err == nil {
		t.Error("d_ri = lambda accepted")
	}
}
