package execute

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"sync"

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

type ExecutionBlock struct {
	PreStateRoot  common.Hash
	PostStateRoot common.Hash
	GasUsed       uint64
	Witness       *stateless.Witness
	Header        types.Header
	Transactions  types.Transactions
}

type ExecutionTrace struct {
	Blocks []ExecutionBlock `ssz:"-"`
}

type CalldataInput struct {
	Blocks []struct {
		PreStateRoot  common.Hash
		PostStateRoot common.Hash
		GasUsed       *big.Int
	}
	CompressedPayload []byte
}

func DecodeExecutionInput(input []byte) (*ExecutionTrace, error) {
	var raw CalldataInput
	if err := rlp.DecodeBytes(input, &raw); err != nil {
		return nil, fmt.Errorf("invalid input format: %w", err)
	}

	gz, err := gzip.NewReader(bytes.NewReader(raw.CompressedPayload))
	if err != nil {
		return nil, fmt.Errorf("invalid compression: %w", err)
	}
	defer gz.Close()

	decompressed, err := io.ReadAll(gz)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress payload: %w", err)
	}

	var trace ExecutionTrace
	if err := ssz.Unmarshal(decompressed, &trace); err != nil {
		return nil, fmt.Errorf("invalid trace format: %w", err)
	}

	// Validate block count matches
	if len(raw.Blocks) != len(trace.Blocks) {
		return nil, errors.New("block count mismatch between input and trace")
	}

	// Merge the data
	for i := range raw.Blocks {
		trace.Blocks[i].PreStateRoot = raw.Blocks[i].PreStateRoot
		trace.Blocks[i].PostStateRoot = raw.Blocks[i].PostStateRoot
		trace.Blocks[i].GasUsed = raw.Blocks[i].GasUsed.Uint64()
	}

	return &trace, nil
}

// ExecutePrecompile handles the full execution flow from input decoding to state validation
func ExecutePrecompile(input []byte, chainConfig *params.ChainConfig, vmConfig *vm.Config) ([]byte, error) {
	trace, err := DecodeExecutionInput(input)
	if err != nil {
		return []byte{0}, err
	}

	// First verify state root continuity for entire sequence
	for i := 1; i < len(trace.Blocks); i++ {
		prevPost := trace.Blocks[i-1].PostStateRoot
		currPre := trace.Blocks[i].PreStateRoot
		if prevPost != currPre {
			return []byte{0}, fmt.Errorf("state root discontinuity at block %d (prev: %x, curr: %x)",
				i, prevPost, currPre)
		}
	}

	// Then perform concurrent execution validation
	var (
		wg          sync.WaitGroup
		errCh       = make(chan error, len(trace.Blocks))
		ctx, cancel = context.WithCancel(context.Background())
	)
	defer cancel()

	for i := range trace.Blocks {
		wg.Add(1)
		go func(block *ExecutionBlock) {
			defer wg.Done()

			select {
			case <-ctx.Done():
				return
			default:
			}

			if block.Header.GasUsed != block.GasUsed {
				errCh <- fmt.Errorf("block %x gas used mismatch", block.Header.Hash())
				cancel()
				return
			}

			blockInst := types.NewBlock(
				&block.Header,
				&types.Body{Transactions: block.Transactions},
				nil,
				trie.NewStackTrie(nil),
			)

			stateRoot, _, err := core.ExecuteStateless(
				chainConfig,
				*vmConfig,
				blockInst,
				block.Witness,
			)
			if err != nil {
				errCh <- fmt.Errorf("block %x execution failed: %w", block.Header.Hash(), err)
				cancel()
				return
			}

			if stateRoot != block.PostStateRoot {
				errCh <- fmt.Errorf("block %x state root mismatch (got: %x, want: %x)",
					block.Header.Hash(), stateRoot, block.PostStateRoot)
				cancel()
				return
			}

		}(&trace.Blocks[i])
	}

	go func() {
		wg.Wait()
		close(errCh)
	}()

	var errors []string
	for err := range errCh {
		errors = append(errors, err.Error())
	}

	if len(errors) > 0 {
		return []byte{0}, fmt.Errorf("execution failed: %s", strings.Join(errors, "; "))
	}

	return []byte{1}, nil
}
