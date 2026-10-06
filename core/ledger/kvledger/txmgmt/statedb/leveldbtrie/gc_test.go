/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package leveldbtrie

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	db "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/stretchr/testify/require"
)

// TestTheCollectorReclaimsTheNodesOfTheVersionsNothingReads commits a block at a
// time with nothing collecting the nodes of the versions behind, and opens the
// channel again.
//
// Every commit writes the tree as it is and leaves the tree as it was where it
// is, so a channel of five blocks holds the nodes of five versions of the state,
// of which one is the state. Opening the channel collects them: the state the
// channel is at reads the same before and after, and everything else is gone.
func TestTheCollectorReclaimsTheNodesOfTheVersionsNothingReads(t *testing.T) {
	dbPath := t.TempDir()
	const channel = "gc-at-open"

	before := providerAt(t, dbPath, &Conf{}, nil)
	require.NoError(t, commitBlocks(before, channel, blocksOf(1, 5, 10)...))
	fiveVersions := countNodes(t, before, channel)
	before.Close()

	reporter := &recordedRoots{}
	after := providerAt(t, dbPath, &Conf{}, reporter)
	defer after.Close()

	vdb, err := after.GetDBHandle(channel, nil)
	require.NoError(t, err)
	require.NoError(t, vdb.Open())

	require.Len(t, reporter.collections, 1, "a channel is collected when it is opened")
	require.Positive(t, reporter.collections[0].reclaimed, "the versions behind the one that is read are garbage")
	require.Less(t, countNodes(t, after, channel), fiveVersions,
		"the tree of one version is fewer nodes than the tree of five of them")

	// The state the channel is at is whole: every key of the block it was
	// committed at reads, which is what the collector must not have collected.
	for i := range 10 {
		value, err := vdb.GetState("ns", keyOf(i))
		require.NoError(t, err)
		require.Equal(t, valueOf(5, i), value.Value)
	}
}

// TestTheCollectorStopsTheDatabaseGrowingWithEveryCommit commits a hundred
// blocks with nothing collecting them, and the same hundred with everything
// collected after every one of them, and counts the nodes left in the two
// databases.
//
// A version of the tree is written beside every version before it, so a database
// that is never collected grows with every block a peer commits to, and a peer
// that has been running for a year holds a hundred times the nodes of one that
// has just started. A database that is collected holds the tree the channel is
// at and nothing else.
func TestTheCollectorStopsTheDatabaseGrowingWithEveryCommit(t *testing.T) {
	const channel = "gc-interval"

	neverCollected := newTestEnvWithConf(t, &Conf{}, nil)
	collected := newTestEnvWithConf(t, &Conf{GCIntervalBlocks: 1}, nil)

	require.NoError(t, commitBlocks(neverCollected, channel, blocksOf(1, 1, 10)...))
	oneVersion := countNodes(t, neverCollected, channel)

	require.NoError(t, commitBlocks(neverCollected, channel, blocksOf(2, 100, 10)...))
	require.NoError(t, commitBlocks(collected, channel, blocksOf(1, 100, 10)...))

	hundredVersions := countNodes(t, neverCollected, channel)
	oneVersionLeft := countNodes(t, collected, channel)

	require.Greater(t, hundredVersions, 10*oneVersion,
		"a version of the tree that nothing reads is left in the database all the same")
	require.Less(t, oneVersionLeft, 2*oneVersion,
		"a hundred blocks of state are no more nodes than the state of one of them")
}

// TestTheCollectorSaysWhatItCollected commits twenty blocks with the
// collector set to run every ten of them, and reads what it said.
//
// The collector is the only thing that knows how many nodes the tree of a channel
// consists of, the only way to find out being a walk of it. It says so both to
// the reporter of the peer and in the database of the channel, where the number
// outlives the run it was counted in.
func TestTheCollectorSaysWhatItCollected(t *testing.T) {
	const channel = "gc-reports"

	reporter := &recordedRoots{}
	provider := newTestEnvWithConf(t, &Conf{GCIntervalBlocks: 10}, reporter)

	require.NoError(t, commitBlocks(provider, channel, blocksOf(1, 20, 10)...))

	// Three runs: at the open of the channel, which found nothing to collect, and
	// after the tenth block and after the twentieth, which is the last one.
	require.Len(t, reporter.collections, 3)
	require.Equal(t, collection{}, reporter.collections[0])
	for _, collected := range reporter.collections[1:] {
		require.Positive(t, collected.live, "the tree of a committed block has nodes of its own")
		require.Positive(t, collected.reclaimed, "nine blocks of state behind the one that is read are garbage")
	}

	last := reporter.collections[len(reporter.collections)-1]
	require.Equal(t, last.live, countNodes(t, provider, channel),
		"what the collector leaves in the database is what it says it has left")

	vdb, err := provider.GetDBHandle(channel, nil)
	require.NoError(t, err)
	live, err := vdb.(*versionedDB).metadata.liveNodes()
	require.NoError(t, err)
	require.NotNil(t, live)
	require.Equal(t, last.live, *live, "the number of live nodes is written down, and is the number the walk came to")
}

// TestTheCollectorReportsToTheReporterOfTheCommitItFollows commits a block
// through the path that hands out the roots of a block and of its transactions,
// to a store whose provider reports to nobody, and looks at who was told what.
//
// The nodes the collector reclaims belong to the version the block was committed
// to, so what was reclaimed and how long it took is a measurement of that block,
// and a measurement of a block belongs with the root of that block. Reported to
// the reporter of the provider instead, it is a measurement of one block among
// the roots of every other one, and neither of the two series names the block it
// came from.
func TestTheCollectorReportsToTheReporterOfTheCommitItFollows(t *testing.T) {
	const channel = "gc-of-the-commit"

	// The provider reports to nobody, so whatever the store says of a commit it
	// says to the reporter that commit was given.
	provider := newTestEnvWithConf(t, &Conf{GCIntervalBlocks: 1}, nil)
	vdb, err := provider.GetDBHandle(channel, nil)
	require.NoError(t, err)
	require.NoError(t, vdb.Open())

	// One block before, so that the version behind the one the next block commits
	// to is there to be reclaimed.
	require.NoError(t, vdb.ApplyUpdates(
		updateBatch("ns", "key1", []byte("value1"), version.NewHeight(1, 1)),
		version.NewHeight(1, 1),
	))

	second := updateBatch("ns", "key2", []byte("value2"), version.NewHeight(2, 1))
	reporter := &recordedRoots{}
	require.NoError(t, vdb.(statedb.IntermediateRoots).ApplyUpdatesWithRoots(
		second, version.NewHeight(2, 1), []*statedb.UpdateBatch{second}, reporter,
	))

	require.Len(t, reporter.collections, 1, "the collection of a commit is reported to the reporter of that commit")
	require.Positive(t, reporter.collections[0].reclaimed, "the version behind the one that is read is garbage")
}

// TestTheWalkTurnsBackThePanicsOfTheWalkAndNotTheOnesOfItsCaller walks the tree
// of a channel twice: once over a whole tree, with a visitor of the walk that
// panics of its own accord, and once over a tree the database holds something
// else in place of a node of.
//
// Two failures reach a walk of the state of a channel, and they are not the same
// thing. The database may not hold the tree the channel is committed at, which is
// the state of the channel being broken and is what an operator has to be told
// about. Or the check made of the tree may be wrong, which is the check being
// wrong and says nothing whatever about the state: reported as corruption of the
// state, it sends an operator to look at a database that is whole.
func TestTheWalkTurnsBackThePanicsOfTheWalkAndNotTheOnesOfItsCaller(t *testing.T) {
	dbPath := t.TempDir()
	const channel = "walk"

	reporter := &recordedRoots{}
	before := providerAt(t, dbPath, &Conf{}, reporter)
	require.NoError(t, commitBlocks(before, channel, blocksOf(1, 1, 10)...))
	root := reporter.blockRoots[len(reporter.blockRoots)-1].root
	before.Close()

	t.Run("a panic of the caller is left to the caller", func(t *testing.T) {
		provider := providerAt(t, dbPath, &Conf{}, nil)
		defer provider.Close()

		vdb, err := provider.GetDBHandle(channel, nil)
		require.NoError(t, err)
		require.NoError(t, vdb.Open())
		tree, err := vdb.(*versionedDB).tree()
		require.NoError(t, err)

		require.PanicsWithValue(t, "the check of the walk is wrong", func() {
			_ = walkNodes(tree, func(common.Hash, []byte) error {
				panic("the check of the walk is wrong")
			})
		}, "a failure of the check is the check's own and not a report about the state")
	})

	t.Run("a node the database holds something else under is reported", func(t *testing.T) {
		altered := putWhatIsNotANodeUnder(t, dbPath, channel, root)

		provider := providerAt(t, dbPath, &Conf{}, nil)
		defer provider.Close()

		vdb, err := provider.GetDBHandle(channel, nil)
		require.NoError(t, err)
		err = vdb.Open()
		require.Error(t, err, "the root is in the database, so what is not what it is called is a node under it")
		require.Contains(t, err.Error(), "not the tree its nodes are said to be")
		require.Contains(t, err.Error(), common.Bytes2Hex(altered[:]),
			"the report has to name the node that is not the one it is called by")
	})
}

// blocksOf returns the batches of the given number of blocks, the first of them
// the block of the given number, each writing the given number of keys.
func blocksOf(fromBlockNum uint64, count int, keys int) []*statedb.UpdateBatch {
	batches := make([]*statedb.UpdateBatch, 0, count)
	for blockNum := fromBlockNum; blockNum < fromBlockNum+uint64(count); blockNum++ {
		batch := statedb.NewUpdateBatch()
		for i := range keys {
			batch.Put("ns", keyOf(i), valueOf(blockNum, i), version.NewHeight(blockNum, uint64(i)))
		}
		batches = append(batches, batch)
	}
	return batches
}

// keyOf returns the name of the i-th key of a block.
func keyOf(i int) string {
	return fmt.Sprintf("key-%03d", i)
}

// valueOf returns the value the i-th key of a block is given. It is a different
// value in every block, so that every commit is a commit of something.
func valueOf(blockNum uint64, i int) []byte {
	return fmt.Appendf(nil, "value-of-block-%d-for-%s-0123456789abcdef0123456789abcdef", blockNum, keyOf(i))
}

// putWhatIsNotANodeUnder puts something that is not a node of a tree in the
// database where a node of the tree of the channel is, and returns the hash that
// node was called by.
//
// The root is left alone on purpose: a channel whose root is gone, or is not a
// node, is refused the moment it is opened, before anything walks its tree. What
// is being tested here is what the walk of go-ethereum makes of a node of the
// tree under the root that the database holds something else under, and it is a
// panic rather than an error: the walk of a state has to be able to report what
// it found.
func putWhatIsNotANodeUnder(t testing.TB, dbPath string, channel string, root string) common.Hash {
	t.Helper()

	var altered common.Hash
	inDatabase(t, dbPath, channel, func(handle db.DBHandle) {
		key := anyNodeKeyButTheRootOf(t, handle, root)
		hash, ok := NewNodeDB(handle).nodeHash(key)
		require.True(t, ok, "the key of the node of the tree is the key of a node")
		altered = hash
		require.NoError(t, handle.Put(key, []byte("this is not a node of any tree"), true))
	})
	return altered
}

// anyNodeKeyButTheRootOf returns the key of one of the nodes of the tree in the
// database that is not the key of its root.
func anyNodeKeyButTheRootOf(t testing.TB, handle db.DBHandle, root string) []byte {
	t.Helper()

	start, end := nodeKeyRange()
	itr, err := handle.GetIterator(start, end)
	require.NoError(t, err)

	keys := make([][]byte, 0)
	for itr.Next() {
		keys = append(keys, itr.Key())
	}
	require.NoError(t, itr.Error())
	itr.Release()

	hash := common.HexToHash(root)
	ofRoot := nodeKey(hash[:])
	for _, key := range keys {
		if bytes.Equal(key, ofRoot) {
			continue
		}
		return key
	}
	require.FailNow(t, "the tree of a channel of ten keys is more than its root")
	return nil
}

// countNodes returns the number of nodes the state of the channel keeps in its
// database.
func countNodes(t testing.TB, provider *versionedDBProvider, channel string) int {
	t.Helper()

	start, end := nodeKeyRange()
	itr, err := provider.dbProvider.GetDBHandle(channel).GetIterator(start, end)
	require.NoError(t, err)
	defer itr.Release()

	count := 0
	for itr.Next() {
		count++
	}
	require.NoError(t, itr.Error())
	return count
}
