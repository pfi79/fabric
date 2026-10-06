/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package leveldbtrie

import (
	"sort"
	"testing"

	db "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/statekvdb"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

// TestCommitWritesNodesAndMetadataInOneTransaction commits a block and counts
// the transactions of the database it takes. It has to take one: the nodes of a
// version and the root that names them are of no use apart from each other, and
// a write that reaches one of them without the other leaves a database that
// reads as a state it is not.
func TestCommitWritesNodesAndMetadataInOneTransaction(t *testing.T) {
	provider := newTestEnv(t)
	handle := &countingHandle{DBHandle: provider.dbProvider.GetDBHandle("one-transaction")}
	vdb := newVersionedDB(handle, "one-transaction", provider.dbPath, provider.conf, nil)

	batch := updateBatch("ns", "key1", []byte("value1"), version.NewHeight(1, 1))
	require.NoError(t, vdb.ApplyUpdates(batch, version.NewHeight(1, 1)))

	require.Equal(t, 1, handle.writes,
		"the nodes of a version and the root that names them are one write or nothing")
}

// TestACommitThatIsLostLeavesTheOldStateAndTheOldRoot commits one version of
// the state, then loses the next one, the way a peer that goes away in the
// middle of a write comes back. What has to be left is the version the channel
// was at: its state, its savepoint and its root. Half a version is a state
// nothing ever committed to, and a peer that came back to one would serve it as
// though it were whole.
func TestACommitThatIsLostLeavesTheOldStateAndTheOldRoot(t *testing.T) {
	reporter := &recordedRoots{}
	provider := newTestEnvWithConf(t, &Conf{}, reporter)
	const channel = "lost-commit"

	handle := &countingHandle{DBHandle: provider.dbProvider.GetDBHandle(channel)}
	vdb := newVersionedDB(handle, channel, provider.dbPath, provider.conf, reporter)

	require.NoError(t, vdb.ApplyUpdates(
		updateBatch("ns", "key1", []byte("value1"), version.NewHeight(1, 1)),
		version.NewHeight(1, 1),
	))
	committedRoot := reporter.blockRoots[0]

	// The write of the second version is refused and nothing of it lands: the
	// nodes are in the very batch the root is, so neither of them is.
	handle.loseNextWrite = true
	err := vdb.ApplyUpdates(
		updateBatch("ns", "key2", []byte("value2"), version.NewHeight(1, 2)),
		version.NewHeight(1, 2),
	)
	require.Error(t, err)

	value, err := vdb.GetState("ns", "key1")
	require.NoError(t, err)
	require.Equal(t, []byte("value1"), value.Value, "the state of the committed version is still there")

	value, err = vdb.GetState("ns", "key2")
	require.NoError(t, err)
	require.Nil(t, value, "the state of the version that was lost is not there")

	savePoint, err := vdb.GetLatestSavePoint()
	require.NoError(t, err)
	require.Equal(t, version.NewHeight(1, 1), savePoint, "the savepoint is the one of the committed version")

	require.Equal(t, []rootOfBlock{committedRoot}, reporter.blockRoots,
		"the root moved for a version that was not committed")

	// The channel comes back up on the state that survived, and reads the same
	// state out of the database itself.
	reopened := newVersionedDB(provider.dbProvider.GetDBHandle(channel), channel, provider.dbPath, provider.conf, reporter)
	require.NoError(t, reopened.ApplyUpdates(
		updateBatch("ns", "key3", []byte("value3"), version.NewHeight(1, 3)),
		version.NewHeight(1, 3),
	))
	value, err = reopened.GetState("ns", "key1")
	require.NoError(t, err)
	require.Equal(t, []byte("value1"), value.Value)
	value, err = reopened.GetState("ns", "key2")
	require.NoError(t, err)
	require.Nil(t, value)
}

// countingHandle counts the transactions a database has been asked to write,
// and can be told to lose the next one.
type countingHandle struct {
	db.DBHandle
	writes        int
	loseNextWrite bool
}

func (h *countingHandle) WriteBatch(batch db.Batch, sync bool) error {
	h.writes++
	if h.loseNextWrite {
		h.loseNextWrite = false
		return errors.New("the write was lost on its way to the database")
	}
	return h.DBHandle.WriteBatch(batch, sync)
}

// updateBatch returns a batch that puts the given value under the given key.
func updateBatch(ns, key string, value []byte, vv *version.Height) *statedb.UpdateBatch {
	batch := statedb.NewUpdateBatch()
	batch.Put(ns, key, value, vv)
	return batch
}

// TestSavePointAndRootSurviveAReopen commits two blocks, closes the database and
// opens it again. The channel has to come back at the same height and at the same
// root, and has to read the same state out of it: the root in the database is
// the only thing that says which tree the state of the channel is in, and it is
// the tree the nodes in the database have to be.
func TestSavePointAndRootSurviveAReopen(t *testing.T) {
	dbPath := t.TempDir()
	const channel = "reopen"

	firstBlock := updateBatch("ns", "key1", []byte("value1"), version.NewHeight(1, 1))
	secondBlock := updateBatch("ns", "key2", []byte("value2"), version.NewHeight(2, 1))

	reporter := &recordedRoots{}
	before := providerAt(t, dbPath, &Conf{}, reporter)
	require.NoError(t, commitBlocks(before, channel, firstBlock, secondBlock))
	committedRoot := reporter.blockRoots[len(reporter.blockRoots)-1].root
	before.Close()

	after := providerAt(t, dbPath, &Conf{}, reporter)
	defer after.Close()

	vdb, err := after.GetDBHandle(channel, nil)
	require.NoError(t, err)
	require.NoError(t, vdb.Open())

	savePoint, err := vdb.GetLatestSavePoint()
	require.NoError(t, err)
	require.Equal(t, version.NewHeight(2, 1), savePoint)

	value, err := vdb.GetState("ns", "key1")
	require.NoError(t, err)
	require.Equal(t, []byte("value1"), value.Value)
	value, err = vdb.GetState("ns", "key2")
	require.NoError(t, err)
	require.Equal(t, []byte("value2"), value.Value)

	// The root the reopened store commits to is read by the tree go-ethereum
	// builds over the same entries in a store of its own, which has no part in
	// where this one keeps its nodes.
	thirdBlock := updateBatch("ns", "key3", []byte("value3"), version.NewHeight(3, 1))
	keys, values := treeEntries(t, firstBlock, secondBlock, thirdBlock)
	require.NoError(t, vdb.ApplyUpdates(thirdBlock, version.NewHeight(3, 1)))
	require.Equal(t, referenceRoot(t, keys, values).Hex(), reporter.blockRoots[len(reporter.blockRoots)-1].root)
	require.NotEqual(t, committedRoot, reporter.blockRoots[len(reporter.blockRoots)-1].root)
}

// TestTheOnlyLeafOfTheTreeSurvivesAReopen commits a tree that is one key and one
// short value, which is a tree of a single node, closes the database and opens
// it again.
//
// The node of such a tree is the root of it, and a commit that keeps the leaves
// inside their parents leaves the root where it is instead of writing it down
// under its own hash. A channel whose root is not in its database cannot be
// opened at all, so a commit that is one node short is a peer that comes back up
// with nothing.
func TestTheOnlyLeafOfTheTreeSurvivesAReopen(t *testing.T) {
	dbPath := t.TempDir()
	const channel = "oneleaf"
	batch := updateBatch("ns", "key", []byte("value"), version.NewHeight(1, 1))

	before := providerAt(t, dbPath, &Conf{}, nil)
	require.NoError(t, commitBlocks(before, channel, batch))
	before.Close()

	after := providerAt(t, dbPath, &Conf{}, nil)
	defer after.Close()

	vdb, err := after.GetDBHandle(channel, nil)
	require.NoError(t, err)
	require.NoError(t, vdb.Open())

	value, err := vdb.GetState("ns", "key")
	require.NoError(t, err)
	require.Equal(t, []byte("value"), value.Value)
}

// providerAt gives a provider over the database at the given path, for the tests
// that close it and open it again.
func providerAt(t testing.TB, dbPath string, conf *Conf, reporter statedb.RootReporter) *versionedDBProvider {
	t.Helper()

	provider, err := NewProvider(dbPath, db.GoLevelDB, conf, reporter)
	require.NoError(t, err)
	return provider.(*versionedDBProvider)
}

// commitBlocks commits the given blocks to the given channel of the given
// provider, each of them as the commit of a block of its own.
func commitBlocks(provider *versionedDBProvider, channel string, blocks ...*statedb.UpdateBatch) error {
	vdb, err := provider.GetDBHandle(channel, nil)
	if err != nil {
		return err
	}
	if err := vdb.Open(); err != nil {
		return err
	}
	for blockNum, batch := range blocks {
		if err := vdb.ApplyUpdates(batch, version.NewHeight(uint64(blockNum+1), 1)); err != nil {
			return err
		}
	}
	return nil
}

// treeEntries returns the entries of the tree that committing the given batches
// comes to, in the form a tree of go-ethereum can be built from.
//
// The namespaces and the keys of a batch are written out in byte order: the order
// the batch hands them out in is the order of the map behind it, and the same
// entries have to come out in the same order every time they are asked for.
func treeEntries(t testing.TB, batches ...*statedb.UpdateBatch) (keys, values [][]byte) {
	t.Helper()

	for _, batch := range batches {
		namespaces := batch.GetUpdatedNamespaces()
		sort.Strings(namespaces)
		for _, ns := range namespaces {
			updates := batch.GetUpdates(ns)
			batchKeys := make([]string, 0, len(updates))
			for key := range updates {
				batchKeys = append(batchKeys, key)
			}
			sort.Strings(batchKeys)
			for _, key := range batchKeys {
				value, err := statekvdb.EncodeValue(updates[key])
				require.NoError(t, err)
				keys = append(keys, encodeTreeKey(ns, key))
				values = append(values, value)
			}
		}
	}
	return keys, values
}
