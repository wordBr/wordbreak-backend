// Package signer is the referee's cryptographic authority. It produces the EIP-712
// signatures that WordBreakPools.settle() verifies before paying out — the bridge between
// the off-chain game result and the on-chain money. The domain and types here MUST match
// the contract exactly (name "WordBreakPools", version "1", the Settlement struct).
package signer

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

// Signer holds the referee key and the domain it signs for.
type Signer struct {
	key               *ecdsa.PrivateKey
	address           common.Address
	chainID           *big.Int
	verifyingContract common.Address
}

// New builds a Signer from a hex private key, the chain id, and the deployed pool address.
func New(privHex string, chainID int64, verifyingContract string) (*Signer, error) {
	key, err := crypto.HexToECDSA(strings.TrimPrefix(privHex, "0x"))
	if err != nil {
		return nil, fmt.Errorf("invalid referee private key: %w", err)
	}
	if !common.IsHexAddress(verifyingContract) {
		return nil, fmt.Errorf("invalid verifying contract address: %q", verifyingContract)
	}
	return &Signer{
		key:               key,
		address:           crypto.PubkeyToAddress(key.PublicKey),
		chainID:           big.NewInt(chainID),
		verifyingContract: common.HexToAddress(verifyingContract),
	}, nil
}

// Address is the referee address the contract must have configured.
func (s *Signer) Address() common.Address { return s.address }

// Digest returns the 32-byte EIP-712 digest for a settlement (the value that gets signed).
func (s *Signer) Digest(roundID *big.Int, winners []common.Address, amounts []*big.Int) ([]byte, error) {
	td := s.typedData(roundID, winners, amounts)
	domainSep, err := td.HashStruct("EIP712Domain", td.Domain.Map())
	if err != nil {
		return nil, fmt.Errorf("hash domain: %w", err)
	}
	msgHash, err := td.HashStruct(td.PrimaryType, td.Message)
	if err != nil {
		return nil, fmt.Errorf("hash message: %w", err)
	}
	raw := make([]byte, 0, 2+len(domainSep)+len(msgHash))
	raw = append(raw, 0x19, 0x01)
	raw = append(raw, domainSep...)
	raw = append(raw, msgHash...)
	return crypto.Keccak256(raw), nil
}

// SignSettlement signs a settlement result and returns the 65-byte signature (r||s||v with
// v in {27,28}, the form Solidity's ecrecover / OZ ECDSA.recover expects).
func (s *Signer) SignSettlement(roundID *big.Int, winners []common.Address, amounts []*big.Int) ([]byte, error) {
	if len(winners) != len(amounts) {
		return nil, fmt.Errorf("winners/amounts length mismatch: %d vs %d", len(winners), len(amounts))
	}
	digest, err := s.Digest(roundID, winners, amounts)
	if err != nil {
		return nil, err
	}
	sig, err := crypto.Sign(digest, s.key)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	// go-ethereum returns v as 0/1; Solidity ecrecover wants 27/28.
	sig[64] += 27
	return sig, nil
}

func (s *Signer) typedData(roundID *big.Int, winners []common.Address, amounts []*big.Int) apitypes.TypedData {
	ws := make([]interface{}, len(winners))
	for i, w := range winners {
		ws[i] = w.Hex()
	}
	as := make([]interface{}, len(amounts))
	for i, a := range amounts {
		as[i] = a
	}
	return apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
			"Settlement": {
				{Name: "roundId", Type: "uint256"},
				{Name: "winners", Type: "address[]"},
				{Name: "amounts", Type: "uint256[]"},
			},
		},
		PrimaryType: "Settlement",
		Domain: apitypes.TypedDataDomain{
			Name:              "WordBreakPools",
			Version:           "1",
			ChainId:           (*math.HexOrDecimal256)(s.chainID),
			VerifyingContract: s.verifyingContract.Hex(),
		},
		Message: apitypes.TypedDataMessage{
			"roundId": roundID,
			"winners": ws,
			"amounts": as,
		},
	}
}
