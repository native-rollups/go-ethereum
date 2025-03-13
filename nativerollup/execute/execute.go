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
	"github.com/ethereum/go-ethereum/consensus/beacon"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/prysmaticlabs/go-ssz"
)

type ExecutionBlock struct {
	PreStateRoot  common.Hash
	PostStateRoot common.Hash
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

func decodeExecutionInput(input []byte) (*ExecutionTrace, error) {
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
		trace.Blocks[i].Header.GasUsed = raw.Blocks[i].GasUsed.Uint64()
	}

	return &trace, nil
}

// ExecutePrecompile handles the full execution flow from input decoding to state validation
func ExecutePrecompile(input []byte, chainConfig *params.ChainConfig, vmConfig *vm.Config) ([]byte, error) {
	trace, err := decodeExecutionInput(input)
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

	// Then perform concurrent execution verification
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

			blockInst := types.NewBlock(
				&block.Header,
				&types.Body{Transactions: block.Transactions},
				nil,
				trie.NewStackTrie(nil),
			)

			stateRoot, _, err := executeStateless(
				chainConfig,
				*vmConfig,
				blockInst,
				block.PreStateRoot,
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

// Modified from core.ExecuteStateless to also verify pre-state root
func executeStateless(config *params.ChainConfig, vmconfig vm.Config, block *types.Block, preStateRoot common.Hash, witness *stateless.Witness) (common.Hash, common.Hash, error) {
	// Sanity check if the supplied block accidentally contains a set root or
	// receipt hash. If so, be very loud, but still continue.
	if block.Root() != (common.Hash{}) {
		log.Error("stateless runner received state root it's expected to calculate (faulty consensus client)", "block", block.Number())
	}
	if block.ReceiptHash() != (common.Hash{}) {
		log.Error("stateless runner received receipt root it's expected to calculate (faulty consensus client)", "block", block.Number())
	}

	// Create and populate the state database to serve as the stateless backend
	memdb := witness.MakeHashDB()
	db, err := state.New(witness.Root(), state.NewDatabase(triedb.NewDatabase(memdb, triedb.HashDefaults), nil))
	if err != nil {
		return common.Hash{}, common.Hash{}, err
	}

	// Verify that the preStateRoot matches the instantiated state database
	if preStateRoot != db.IntermediateRoot(false) {
		return common.Hash{}, common.Hash{}, fmt.Errorf("Pre-state root does not match instantiated state db root")
	}

	// Create a blockchain that is idle, but can be used to access headers through
	chain, _ := core.NewHeaderChain(memdb, config, beacon.New(ethash.NewFaker()), func() bool { return false })
	processor := core.NewStateProcessor(config, chain)
	validator := core.NewBlockValidator(config, nil) // No chain, we only validate the state, not the block

	// Run the stateless blocks processing and self-validate certain fields
	res, err := processor.Process(block, db, vmconfig)
	if err != nil {
		return common.Hash{}, common.Hash{}, err
	}
	if err = validator.ValidateState(block, db, res, true); err != nil {
		return common.Hash{}, common.Hash{}, err
	}
	// Almost everything validated, but receipt and state root needs to be returned
	receiptRoot := types.DeriveSha(res.Receipts, trie.NewStackTrie(nil))
	stateRoot := db.IntermediateRoot(config.IsEIP158(block.Number()))
	return stateRoot, receiptRoot, nil
}
