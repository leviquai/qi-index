// Package qidecode proves out decoding Qi ledger semantics using go-quai's
// own types, so the indexer never re-implements consensus constants.
package qidecode

import (
	"math/big"

	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/core/types"
	"github.com/dominant-strategies/go-quai/params"
)

// DenominationValue returns the value in qits of a denomination index,
// straight from go-quai's consensus table (core/types/utxo.go).
func DenominationValue(denomination uint8) *big.Int {
	return types.Denominations[denomination]
}

// TrimDeadline returns the block height at which a UTXO of the given
// denomination created at createdHeight will be trimmed by consensus,
// or 0 if the denomination is never trimmed (> MaxTrimDenomination).
func TrimDeadline(denomination uint8, createdHeight uint64) uint64 {
	depth, ok := types.TrimDepths[denomination]
	if !ok {
		return 0
	}
	return createdHeight + depth
}

// CoinbaseLockupDepth returns the lockup duration in blocks for a coinbase
// lockup byte (params/protocol_params.go).
func CoinbaseLockupDepth(lockupByte uint8) uint64 {
	return params.LockupByteToBlockDepth[lockupByte]
}

// ConversionLockPeriod is the number of blocks conversion outputs stay locked.
func ConversionLockPeriod() uint64 {
	return params.ConversionLockPeriod
}

// IsQiAddress reports whether an address is in the Qi ledger scope.
func IsQiAddress(addr common.Address) bool {
	return addr.IsInQiLedgerScope()
}
