package signer

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// A fixed test key (a well-known dev key — never use for real funds).
const testPriv = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"

func TestSignAndRecover(t *testing.T) {
	s, err := New(testPriv, 42220, "0x1111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	roundID := big.NewInt(20260716)
	winners := []common.Address{
		common.HexToAddress("0x00000000000000000000000000000000000000a1"),
		common.HexToAddress("0x00000000000000000000000000000000000000b0"),
	}
	amounts := []*big.Int{
		mustBig("2000000000000000000"), // 2e18
		mustBig("850000000000000000"),  // 0.85e18
	}

	digest, err := s.Digest(roundID, winners, amounts)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	sig, err := s.SignSettlement(roundID, winners, amounts)
	if err != nil {
		t.Fatalf("SignSettlement: %v", err)
	}
	if len(sig) != 65 {
		t.Fatalf("signature length = %d, want 65", len(sig))
	}
	if v := sig[64]; v != 27 && v != 28 {
		t.Fatalf("v = %d, want 27 or 28", v)
	}

	// Recover the signer exactly as Solidity's ecrecover would, and confirm it's the referee.
	recSig := make([]byte, 65)
	copy(recSig, sig)
	recSig[64] -= 27 // back to 0/1 for SigToPub
	pub, err := crypto.SigToPub(digest, recSig)
	if err != nil {
		t.Fatalf("SigToPub: %v", err)
	}
	if got := crypto.PubkeyToAddress(*pub); got != s.Address() {
		t.Fatalf("recovered %s, want referee %s", got.Hex(), s.Address().Hex())
	}
}

// TestDigestMatchesContract is the load-bearing test: it asserts the Go referee produces the
// exact EIP-712 digest that WordBreakPools computes on-chain, for identical inputs. The expected
// value was emitted by the Solidity test `test_LogCanonicalDigest` (chainId 42220, the PROXY
// address below — WordBreakPools is deployed behind a UUPS proxy, and the proxy address is
// what `verifyingContract` binds to since callers always go through it). If this passes,
// contract-accepted signatures are guaranteed.
func TestDigestMatchesContract(t *testing.T) {
	const (
		poolAddr   = "0x6c9fbC0A14D27F72298215b81b21f6c35A7fb506"
		chainID    = 42220
		wantDigest = "0x24d5e29020a5f6cfa44426677689fd8e1df8da6861c45681ada080d0434e8b22"
	)
	s, err := New(testPriv, chainID, poolAddr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	roundID := big.NewInt(20260716)
	winners := []common.Address{
		common.HexToAddress("0x00000000000000000000000000000000000000A1"),
		common.HexToAddress("0x00000000000000000000000000000000000000B0"),
	}
	amounts := []*big.Int{mustBig("2000000000000000000"), mustBig("850000000000000000")}

	digest, err := s.Digest(roundID, winners, amounts)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	got := "0x" + common.Bytes2Hex(digest)
	if got != wantDigest {
		t.Fatalf("digest mismatch:\n go       = %s\n contract = %s", got, wantDigest)
	}
}

func TestSignSettlement_LengthMismatch(t *testing.T) {
	s, _ := New(testPriv, 42220, "0x1111111111111111111111111111111111111111")
	_, err := s.SignSettlement(big.NewInt(1),
		[]common.Address{common.HexToAddress("0x00000000000000000000000000000000000000a1")},
		[]*big.Int{},
	)
	if err == nil {
		t.Fatal("expected length-mismatch error")
	}
}

func mustBig(s string) *big.Int {
	b, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("bad big int: " + s)
	}
	return b
}
