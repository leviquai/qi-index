// Package qidecode wraps go-quai consensus constants and derivation functions
// so the indexer never re-implements protocol logic.
package qidecode

import (
	"math/big"

	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/consensus/misc"
	"github.com/dominant-strategies/go-quai/core/types"
	"github.com/dominant-strategies/go-quai/params"
)

// UTXO source values stored in utxos.source.
const (
	SourceQiTx          = 0
	SourceCoinbaseETX   = 1
	SourceConversionETX = 2
)

// ETX type constants; mirror go-quai core/types/transaction.go.
const (
	ETXTypeCoinbase   = types.CoinbaseType
	ETXTypeConversion = types.ConversionType
)

const (
	TxTypeExternal = types.ExternalTxType
	TxTypeQi       = types.QiTxType
)

func DenominationValue(denomination uint8) *big.Int {
	return types.Denominations[denomination]
}

// TrimDeadline returns createdHeight + TrimDepths[denomination], or 0 if the denomination is never trimmed.
func TrimDeadline(denomination uint8, createdHeight uint64) uint64 {
	depth, ok := types.TrimDepths[denomination]
	if !ok {
		return 0
	}
	return createdHeight + depth
}

func CoinbaseLockupDepth(lockupByte uint8) uint64 {
	return params.LockupByteToBlockDepth[lockupByte]
}

func ConversionLockPeriod() uint64 {
	return params.ConversionLockPeriod
}

func IsQiAddress(addr common.Address) bool {
	return addr.IsInQiLedgerScope()
}

// CoinbaseETXOutputs mirrors state_processor.go: CalculateCoinbaseValueWithLockup → FindMinDenominations.
func CoinbaseETXOutputs(rawValue *big.Int, lockupByte uint8, blockHeight uint64) []DerivedOutput {
	value := params.CalculateCoinbaseValueWithLockup(rawValue, lockupByte, blockHeight)
	lockHeight := blockHeight + params.LockupByteToBlockDepth[lockupByte]
	return derivedOutputs(value, lockHeight)
}

// ConversionETXOutputs mirrors state_processor.go: FindMinDenominations(value), locked for ConversionLockPeriod.
// gasLimit caps output count via (gas-TxGas)/CallValueTransferGas; pass 0 to skip the cap.
func ConversionETXOutputs(value *big.Int, blockHeight, gasLimit uint64) []DerivedOutput {
	lockHeight := blockHeight + params.ConversionLockPeriod
	return derivedOutputsCapped(value, lockHeight, gasLimit)
}

// MaxConversionOutputs returns (gasLimit-TxGas)/CallValueTransferGas, capped at MaxOutputIndex.
func MaxConversionOutputs(gasLimit uint64) uint64 {
	if gasLimit < params.TxGas {
		return 0
	}
	remaining := gasLimit - params.TxGas
	max := remaining / params.CallValueTransferGas
	if max > uint64(types.MaxOutputIndex) {
		max = uint64(types.MaxOutputIndex)
	}
	return max
}

// DerivedOutput is one group of UTXOs sharing a denomination from a coinbase or conversion ETX.
type DerivedOutput struct {
	Denomination uint8
	Count        uint64
	LockHeight   uint64
}

func derivedOutputs(value *big.Int, lockHeight uint64) []DerivedOutput {
	return derivedOutputsCapped(value, lockHeight, 0)
}

func derivedOutputsCapped(value *big.Int, lockHeight, gasLimit uint64) []DerivedOutput {
	denoms := misc.FindMinDenominations(value)
	out := make([]DerivedOutput, 0, len(denoms))
	maxOutputs := uint64(types.MaxOutputIndex) + 1
	if gasLimit > 0 {
		capped := MaxConversionOutputs(gasLimit)
		if capped < maxOutputs {
			maxOutputs = capped
		}
	}
	total := uint64(0)
	for d := types.MaxDenomination; d >= 0; d-- {
		count, ok := denoms[uint8(d)]
		if !ok || count == 0 {
			continue
		}
		if total+count > maxOutputs {
			count = maxOutputs - total
		}
		if count == 0 {
			break
		}
		out = append(out, DerivedOutput{
			Denomination: uint8(d),
			Count:        count,
			LockHeight:   lockHeight,
		})
		total += count
		if total >= maxOutputs {
			break
		}
	}
	return out
}
