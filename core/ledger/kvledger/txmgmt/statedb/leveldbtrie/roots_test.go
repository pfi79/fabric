/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package leveldbtrie

import (
	"testing"

	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/stretchr/testify/require"
)

// TestTheStoreGivesTheRootOfTheStateAfterEveryTransaction commits a block of
// three transactions, the middle of which changes nothing, and looks at what the
// store told the reporter about it.
//
// The root after a transaction is the root of the state the block leaves behind
// if it were to stop there, which is a root of no block and can be had no other
// way. A transaction that leaves the state as it found it leaves that root where
// it was, and a root that did not move is not a root to report.
func TestTheStoreGivesTheRootOfTheStateAfterEveryTransaction(t *testing.T) {
	const channel = "per-tx-roots"
	const blockNum = uint64(7)

	provider := newTestEnv(t)
	vdb, err := provider.GetDBHandle(channel, nil)
	require.NoError(t, err)
	require.NoError(t, vdb.Open())

	first := updateBatch("ns", "key1", []byte("value1"), version.NewHeight(blockNum, 1))
	writesNothing := statedb.NewUpdateBatch()
	last := updateBatch("ns", "key2", []byte("value2"), version.NewHeight(blockNum, 3))

	block := statedb.NewUpdateBatch()
	block.Merge(first)
	block.Merge(last)

	reporter := &recordedRoots{}
	require.NoError(t, vdb.(statedb.IntermediateRoots).ApplyUpdatesWithRoots(
		block, version.NewHeight(blockNum, 1),
		[]*statedb.UpdateBatch{first, writesNothing, last},
		reporter,
	))

	afterFirst, _ := treeEntries(t, first)
	afterLast, _ := treeEntries(t, first, last)

	require.Equal(t, []rootOfTx{
		{blockNum, 0, referenceRoot(t, afterFirst, valuesOf(t, first)).Hex()},
		{blockNum, 2, referenceRoot(t, afterLast, valuesOf(t, first, last)).Hex()},
	}, reporter.txRoots)

	// The root of the block is the root of the state after the last of its
	// transactions, and it is the root the channel is committed at.
	keys, values := treeEntries(t, block)
	require.Equal(t, []rootOfBlock{{blockNum, referenceRoot(t, keys, values).Hex()}}, reporter.blockRoots)

	history, err := vdb.(*versionedDB).metadata.rootHistory()
	require.NoError(t, err)
	require.Equal(t, reporter.blockRoots[0].root, history[0].Hex())
}

// TestTheStoreGivesTheRootsOfABlockNobodyReportsTheRootsOf commits a block of
// transactions with nothing to report to, and with no transactions to speak of.
// The state has to be committed either way: what the roots are for is a
// measurement, and a measurement is not a reason to refuse to commit.
func TestTheStoreGivesTheRootsOfABlockNobodyReportsTheRootsOf(t *testing.T) {
	const channel = "unreported-roots"

	provider := newTestEnv(t)
	vdb, err := provider.GetDBHandle(channel, nil)
	require.NoError(t, err)
	require.NoError(t, vdb.Open())
	withRoots, ok := vdb.(statedb.IntermediateRoots)
	require.True(t, ok, "the store is the one that can answer for the transactions of a block")

	first := updateBatch("ns", "key1", []byte("value1"), version.NewHeight(1, 1))
	block := statedb.NewUpdateBatch()
	block.Merge(first)

	t.Run("nothing to report to", func(t *testing.T) {
		require.NoError(t, withRoots.ApplyUpdatesWithRoots(
			block, version.NewHeight(1, 1), []*statedb.UpdateBatch{first}, nil,
		))

		value, err := vdb.GetState("ns", "key1")
		require.NoError(t, err)
		require.Equal(t, []byte("value1"), value.Value)

		savePoint, err := vdb.GetLatestSavePoint()
		require.NoError(t, err)
		require.Equal(t, version.NewHeight(1, 1), savePoint)
	})

	t.Run("no transactions to speak of", func(t *testing.T) {
		second := updateBatch("ns", "key2", []byte("value2"), version.NewHeight(2, 1))
		reporter := &recordedRoots{}

		// A block that comes as one batch is one transaction as far as the store
		// is concerned, which is the whole of the block as far as it is concerned.
		require.NoError(t, withRoots.ApplyUpdatesWithRoots(second, version.NewHeight(2, 1), nil, reporter))

		require.Empty(t, reporter.txRoots)
		keys, values := treeEntries(t, block, second)
		require.Equal(t, []rootOfBlock{{2, referenceRoot(t, keys, values).Hex()}}, reporter.blockRoots)
	})
}

// valuesOf returns the values the given batches put, in the order treeEntries
// returns the keys of the same batches in.
func valuesOf(t testing.TB, batches ...*statedb.UpdateBatch) [][]byte {
	t.Helper()

	_, values := treeEntries(t, batches...)
	return values
}
