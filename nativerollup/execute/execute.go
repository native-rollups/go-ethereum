package execute

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/prysmaticlabs/go-ssz"
)

type ExecutionTrace struct {
	Witness      *stateless.Witness
	BlockHeader  types.Header `ssz:"-"`
	Transactions []*types.Transaction
}

type ExecutionPayload struct {
	Witness      *stateless.Witness
	Header       types.Header
	Transactions types.Transactions
}

type ExecutionInput struct {
	PreStateRoot  common.Hash
	PostStateRoot common.Hash
	GasUsed       uint64
}

type CalldataInput struct {
	PreStateRoot      common.Hash
	PostStateRoot     common.Hash
	CompressedPayload []byte
	GasUsed           *big.Int
}

func DecodeExecutionInput(input []byte) (*ExecutionInput, *ExecutionPayload, error) {
	// Decode RLP wrapper
	var raw CalldataInput
	if err := rlp.DecodeBytes(input, &raw); err != nil {
		return nil, nil, fmt.Errorf("invalid input format: %w", err)
	}

	// Decompress payload and read all bytes
	gz, err := gzip.NewReader(bytes.NewReader(raw.CompressedPayload))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid compression: %w", err)
	}
	defer gz.Close()

	decompressed, err := io.ReadAll(gz)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decompress payload: %w", err)
	}

	// Decode SSZ structure from the decompressed bytes
	var trace ExecutionTrace
	if err := ssz.Unmarshal(decompressed, &trace); err != nil {
		return nil, nil, fmt.Errorf("invalid trace format: %w", err)
	}

	inputData := &ExecutionInput{
		PreStateRoot:  raw.PreStateRoot,
		PostStateRoot: raw.PostStateRoot,
		GasUsed:       raw.GasUsed.Uint64(),
	}

	payload := &ExecutionPayload{
		Witness:      trace.Witness,
		Header:       trace.BlockHeader,
		Transactions: trace.Transactions,
	}

	return inputData, payload, nil
}

// ExecutePrecompile handles the full execution flow from input decoding to state validation
func ExecutePrecompile(input []byte, chainConfig *params.ChainConfig, vmConfig *vm.Config) ([]byte, error) {
	execInput, payload, err := DecodeExecutionInput(input)
	if err != nil {
		return nil, err
	}

	// Construct block using payload data
	header := payload.Header
	header.GasUsed = execInput.GasUsed
	block := types.NewBlock(
		&header,
		&types.Body{Transactions: payload.Transactions},
		nil,
		trie.NewStackTrie(nil),
	)

	// Execute the block
	stateRoot, _, err := core.ExecuteStateless(chainConfig, *vmConfig, block, payload.Witness)
	if err != nil {
		return nil, fmt.Errorf("failed to execute block: %w", err)
	}

	// Validate final state root
	if stateRoot != execInput.PostStateRoot {
		return nil, errors.New("final state root mismatch")
	}

	return []byte{1}, nil
}
