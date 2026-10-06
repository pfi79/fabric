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
	"github.com/hyperledger/fabric/common/ledger/dataformat"
	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/stretchr/testify/require"
)

// TestOpeningOnARootTheDatabaseDoesNotHoldFails takes the node of the root away
// from a channel that has been committed to, and opens the channel again.
//
// A channel whose root is not in its database is a broken channel, and the root
// is the only thing that says so. Opening it as an empty state instead would
// serve a state of no keys that nobody ever committed to, which looks to every
// caller exactly like the state a channel that has never been written to has.
//
// The report has to name the channel and the database to look in beside the root
// that is not there: the one holding the report is an operator, and every
// channel of the peer has a database of its own to look in.
func TestOpeningAChannelWhoseRootIsGoneFails(t *testing.T) {
	dbPath := t.TempDir()
	const channel = "noroot"

	reporter := &recordedRoots{}
	before := providerAt(t, dbPath, &Conf{}, reporter)
	require.NoError(t, commitBlocks(before, channel,
		updateBatch("ns", "key1", []byte("value1"), version.NewHeight(1, 1)),
		updateBatch("ns", "key2", []byte("value2"), version.NewHeight(2, 1)),
	))
	root := reporter.blockRoots[len(reporter.blockRoots)-1].root
	before.Close()

	// The node of the root is a node of the tree like any other, and a channel
	// that has lost it has lost the tree it is committed at.
	removeNode(t, dbPath, channel, common.HexToHash(root))

	after := providerAt(t, dbPath, &Conf{}, nil)
	defer after.Close()

	vdb, err := after.GetDBHandle(channel, nil)
	require.NoError(t, err)
	err = vdb.Open()
	require.Error(t, err)
	require.Contains(t, err.Error(), root, "the report has to name the root that is not there")
	require.Contains(t, err.Error(), channel, "the report has to name the channel it is about")
	require.Contains(t, err.Error(), dbPath, "the report has to name the database of that channel to look in")
}

// TestVerifyOnOpenFindsANodeThatIsNotTheNodeItIsCalled puts one value of a
// channel in place of another in the one node of the tree that holds it, and
// opens the channel, once with the walk of the whole tree asked for and once
// without.
//
// What the leaf holds changes, and with it what every key of the channel reads.
// Nothing about that is broken as far as a read can tell: the node is a leaf, it
// is where the tree says a leaf is, the tree opens and answers out of it. The
// name the node is kept under and the hash of what it holds are two different
// things, and it is the walk of the whole tree that says so.
func TestVerifyOnOpenFindsANodeThatIsNotTheNodeItIsCalled(t *testing.T) {
	dbPath := t.TempDir()
	const channel = "verify"

	// Two values of the same length, so that one can be put in place of the other
	// without the node holding it changing shape.
	value1, value2 := string(longValue("value1")), string(longValue("value2"))

	// One block, so that the database holds the nodes of one version of the tree
	// and no other: the leaf that is altered below has to be the only node that
	// holds the value at all.
	both := statedb.NewUpdateBatch()
	both.Put("ns", "key1", []byte(value1), version.NewHeight(1, 1))
	both.Put("ns", "key2", []byte(value2), version.NewHeight(1, 2))

	before := providerAt(t, dbPath, &Conf{}, nil)
	require.NoError(t, commitBlocks(before, channel, both))
	before.Close()

	altered := alterTheNodeHolding(t, dbPath, channel, value1, value2)
	require.NotEqual(t, common.Hash{}, altered)

	t.Run("the whole tree is walked", func(t *testing.T) {
		// Nothing is said about the walk: a configuration that says nothing at all
		// is the one that gets the tree walked, and the walk is the only check that
		// finds a node that is not the one it is called by.
		provider := providerAt(t, dbPath, &Conf{}, nil)
		defer provider.Close()

		vdb, err := provider.GetDBHandle(channel, nil)
		require.NoError(t, err)
		err = vdb.Open()
		require.Error(t, err, "the walk of the tree has to find a node that is not the one it is called by")
		require.Contains(t, err.Error(), altered.Hex())
	})

	t.Run("the whole tree is not walked", func(t *testing.T) {
		// Nothing is promised of a database nobody asked to be checked, and the
		// channel opens. It is the reads that answer with the values of the keys
		// next to them, which is what the walk is asked for in the first place.
		//
		// A key of the configuration that is a plain bool cannot be told from a key
		// that was left out, so telling the store not to walk its tree takes naming
		// a key of the configuration as well: a configuration that says nothing at
		// all is the one of the specification, and the specification walks the tree.
		provider := providerAt(t, dbPath, &Conf{VerifyOnOpen: false, ExactMetrics: true}, nil)
		defer provider.Close()

		vdb, err := provider.GetDBHandle(channel, nil)
		require.NoError(t, err)
		require.NoError(t, vdb.Open())

		value, err := vdb.GetState("ns", "key1")
		require.NoError(t, err)
		require.Equal(t, []byte(value2), value.Value, "the key reads the value of the key next to it")
	})
}

// TestADatabaseOfAnotherFormatIsRefused puts a database of another format where
// the state of a channel should be, and opens the channel.
//
// A store that read it as what it is would read the state of a channel out of a
// database written for something else, and hand out a state nobody committed to.
// The format is the only thing in the database that says the two are not the
// same thing, so it has to be looked at before anything else is.
func TestADatabaseOfAnotherFormatIsRefused(t *testing.T) {
	// The format of another version of this store, and the one of a version of
	// it that wrote no format at all: a database of that kind holds state, and
	// this version cannot tell what kind of state it holds.
	for _, format := range []string{"", "0", "2"} {
		t.Run(fmt.Sprintf("format %q", format), func(t *testing.T) {
			dbPath := t.TempDir()
			const channel = "otherformat"

			before := providerAt(t, dbPath, &Conf{}, nil)
			require.NoError(t, commitBlocks(before, channel,
				updateBatch("ns", "key1", []byte("value1"), version.NewHeight(1, 1)),
			))
			before.Close()

			writeFormat(t, dbPath, channel, format)

			provider := providerAt(t, dbPath, &Conf{}, nil)
			defer provider.Close()

			vdb, err := provider.GetDBHandle(channel, nil)
			require.NoError(t, err)
			err = vdb.Open()
			require.Error(t, err)
			require.True(t, dataformat.IsVersionMismatch(err),
				"the report has to be one the ledger can tell a format apart with")
			require.Contains(t, err.Error(), channel, "the report has to name what it is about")
			if format != "" {
				require.Contains(t, err.Error(), format, "the report has to name the format it found")
			}
		})
	}
}

// TestAChannelRestoredFromASnapshotOfNoPublicStateOpens restores a channel from
// a snapshot that holds no public state, and opens it.
//
// What such a snapshot carries is the height the state is consistent upto, and
// nothing else: privacyenabledstate.go:242 hands the store a snapshot with no
// iterator at all when a peer has no public state to restore. Writing the height
// is still a write into the database of the channel, and from then on the
// database is no longer the empty one a channel that was never opened is. It has
// to say which version of this store wrote it, and without that the channel comes
// back as a database of a format nobody knows, which is a channel restored whole
// that refuses to open.
func TestAChannelRestoredFromASnapshotOfNoPublicStateOpens(t *testing.T) {
	dbPath := t.TempDir()
	const channel = "restored"

	restoring := providerAt(t, dbPath, &Conf{}, nil)
	require.NoError(t, restoring.ImportFromSnapshot(channel, version.NewHeight(10, 10), nil))
	restoring.Close()

	reopened := providerAt(t, dbPath, &Conf{}, nil)
	defer reopened.Close()

	vdb, err := reopened.GetDBHandle(channel, nil)
	require.NoError(t, err)
	require.NoError(t, vdb.Open())

	savePoint, err := vdb.GetLatestSavePoint()
	require.NoError(t, err)
	require.Equal(t, version.NewHeight(10, 10), savePoint)
}

// TestAChannelOfNoStateIsOfTheCurrentFormat opens a channel whose database holds
// nothing. It has to open, and it has to be of the format this version writes,
// whatever it is committed to first.
func TestAChannelOfNoStateIsOfTheCurrentFormat(t *testing.T) {
	dbPath := t.TempDir()
	const channel = "newchannel"

	provider := providerAt(t, dbPath, &Conf{}, nil)
	vdb, err := provider.GetDBHandle(channel, nil)
	require.NoError(t, err)
	require.NoError(t, vdb.Open())
	provider.Close()

	require.Equal(t, dbFormatVersion, readFormat(t, dbPath, channel))

	// And the format it was marked with is the one it is checked against.
	reopened := providerAt(t, dbPath, &Conf{}, nil)
	defer reopened.Close()
	vdb, err = reopened.GetDBHandle(channel, nil)
	require.NoError(t, err)
	require.NoError(t, vdb.Open())
}

// TestTheHistoryOfRootsHoldsTheLastRoots commits a block at a time and reads the
// roots of the past versions out of the database.
//
// The history is what the store says about where the channel has been, and it
// is what anything reading the database has to go by: the root the channel is
// committed at alone does not say which roots came before it.
func TestTheHistoryOfRootsHoldsTheLastRoots(t *testing.T) {
	const channel = "history"
	reporter := &recordedRoots{}
	provider := newTestEnvWithConf(t, &Conf{KeepRoots: 3}, reporter)

	require.NoError(t, commitBlocks(provider, channel,
		updateBatch("ns", "key1", []byte("value1"), version.NewHeight(1, 1)),
		updateBatch("ns", "key2", []byte("value2"), version.NewHeight(2, 1)),
		updateBatch("ns", "key3", []byte("value3"), version.NewHeight(3, 1)),
		updateBatch("ns", "key4", []byte("value4"), version.NewHeight(4, 1)),
	))

	committed := make([]string, 0, len(reporter.blockRoots))
	for _, root := range reporter.blockRoots {
		committed = append(committed, root.root)
	}

	vdb, err := provider.GetDBHandle(channel, nil)
	require.NoError(t, err)
	history, err := vdb.(*versionedDB).metadata.rootHistory()
	require.NoError(t, err)

	// The newest root first, and no more of them than the store was told to keep:
	// four blocks were committed to and three roots are written down.
	require.Len(t, history, 3)
	require.Equal(t, committed[3], history[0].Hex())
	require.Equal(t, committed[2], history[1].Hex())
	require.Equal(t, committed[1], history[2].Hex())
}

// TestTheRootOfTheVersionBeforeTheLastOneIsKeptByDefault commits three blocks to
// a store whose configuration says nothing at all, and reads the history of roots
// back.
//
// The number of roots the store writes down is a configuration key, and what the
// key means when nothing is said for it is the specification's: the root the
// channel is at and the one it was at before it. A configuration that says
// nothing is not a configuration of one root, however little it holds, and a
// store that keeps one root loses the only way back to the version behind the
// current one without being told to lose it.
func TestTheRootOfTheVersionBeforeTheLastOneIsKeptByDefault(t *testing.T) {
	const channel = "default-roots"
	reporter := &recordedRoots{}
	provider := newTestEnvWithConf(t, &Conf{}, reporter)

	first := updateBatch("ns", "key1", []byte("value1"), version.NewHeight(1, 1))
	second := updateBatch("ns", "key2", []byte("value2"), version.NewHeight(2, 1))
	third := updateBatch("ns", "key3", []byte("value3"), version.NewHeight(3, 1))
	require.NoError(t, commitBlocks(provider, channel, first, second, third))

	firstAndSecond := statedb.NewUpdateBatch()
	firstAndSecond.Merge(first)
	firstAndSecond.Merge(second)

	vdb, err := provider.GetDBHandle(channel, nil)
	require.NoError(t, err)
	history, err := vdb.(*versionedDB).metadata.rootHistory()
	require.NoError(t, err)

	// Three versions were committed to and two roots are written down: the newest
	// first. What they are is read off a tree of go-ethereum's own building over
	// the same entries, which is where the root of a version comes from in the
	// first place.
	keys, values := treeEntries(t, firstAndSecond, third)
	require.Len(t, history, 2, "the root of the version before the last one is kept as well")
	require.Equal(t, referenceRoot(t, keys, values).Hex(), history[0].Hex())
	keys, values = treeEntries(t, firstAndSecond)
	require.Equal(t, referenceRoot(t, keys, values).Hex(), history[1].Hex())
}

// TestAnEmptyChannelHasTheRootOfATreeWithNoEntries opens a channel that has
// never been committed to, and commits a block that leaves the state as it found
// it. The root of the state is the one every tree with no entries has, both
// before the block and after it.
func TestAnEmptyChannelHasTheRootOfATreeWithNoEntries(t *testing.T) {
	reporter := &recordedRoots{}
	provider := newTestEnvWithConf(t, &Conf{}, reporter)
	const channel = "empty"

	vdb, err := provider.GetDBHandle(channel, nil)
	require.NoError(t, err)
	require.NoError(t, vdb.Open())

	root, err := vdb.(*versionedDB).stateRoot()
	require.NoError(t, err)
	require.Equal(t, emptyRoot, root.Hex())

	value, err := vdb.GetState("ns", "key")
	require.NoError(t, err)
	require.Nil(t, value)

	require.NoError(t, vdb.ApplyUpdates(statedb.NewUpdateBatch(), version.NewHeight(1, 1)))
	require.Equal(t, emptyRoot, reporter.blockRoots[0].root)
}

// TestTheStateOfAChannelEmptiedOfItsKeysReadsBackAsEmpty fills a channel, empties
// it and opens it again.
//
// The root of a tree with no entries is the root of a tree with no nodes, and
// the root the channel was at before is the root of a tree with nodes. A store
// that leaves the root of the tree it used to be in the database opens the
// channel on a tree that says the state it had before it was emptied.
func TestTheStateOfAChannelEmptiedOfItsKeysReadsBackAsEmpty(t *testing.T) {
	dbPath := t.TempDir()
	const channel = "emptied"

	before := providerAt(t, dbPath, &Conf{}, nil)
	require.NoError(t, commitBlocks(before, channel,
		updateBatch("ns", "key1", []byte("value1"), version.NewHeight(1, 1)),
		updateBatch("ns", "key2", []byte("value2"), version.NewHeight(2, 1)),
	))

	emptied := statedb.NewUpdateBatch()
	emptied.Delete("ns", "key1", version.NewHeight(3, 1))
	emptied.Delete("ns", "key2", version.NewHeight(3, 1))
	vdb, err := before.GetDBHandle(channel, nil)
	require.NoError(t, err)
	require.NoError(t, vdb.ApplyUpdates(emptied, version.NewHeight(3, 1)))
	before.Close()

	after := providerAt(t, dbPath, &Conf{}, nil)
	defer after.Close()

	vdb, err = after.GetDBHandle(channel, nil)
	require.NoError(t, err)
	require.NoError(t, vdb.Open())

	root, err := vdb.(*versionedDB).stateRoot()
	require.NoError(t, err)
	require.Equal(t, emptyRoot, root.Hex())

	value, err := vdb.GetState("ns", "key1")
	require.NoError(t, err)
	require.Nil(t, value)
}

// longValue returns a value too long to be folded into the leaf it belongs to,
// so that the leaf is a node of the tree of its own and is kept under its hash.
func longValue(value string) []byte {
	return fmt.Appendf(nil, "%s-0123456789abcdef0123456789abcdef", value)
}

// removeNode deletes the node with the given hash from the state of the channel
// at dbPath.
func removeNode(t testing.TB, dbPath string, channel string, hash common.Hash) {
	t.Helper()

	inDatabase(t, dbPath, channel, func(handle db.DBHandle) {
		require.NoError(t, handle.Delete(nodeKey(hash[:]), true))
	})
}

// alterTheNodeHolding puts one value in the database in place of another of the
// same length, in the one node of the tree that holds it, and returns the hash
// that node is kept under.
//
// A node the database holds under a hash that is not the hash of what it holds is
// a node of some other tree: it decodes as a node, the tree opens on it, and
// every read through it answers with something else. Nothing about it is broken
// in a way a read can tell.
func alterTheNodeHolding(t testing.TB, dbPath string, channel string, held string, put string) common.Hash {
	t.Helper()

	var altered common.Hash
	inDatabase(t, dbPath, channel, func(handle db.DBHandle) {
		start, end := nodeKeyRange()
		itr, err := handle.GetIterator(start, end)
		require.NoError(t, err)
		defer itr.Release()

		found := 0
		for itr.Next() {
			key := itr.Key()
			if !bytes.Contains(itr.Value(), []byte(held)) {
				continue
			}
			require.NoError(t, handle.Put(key, bytes.Replace(itr.Value(), []byte(held), []byte(put), 1), true))
			hash, ok := NewNodeDB(handle).nodeHash(key)
			require.True(t, ok, "the key the value was found under is the key of a node")
			altered = hash
			found++
		}
		require.NoError(t, itr.Error())
		require.Equal(t, 1, found, "the value is held by one node of the tree, and it is the leaf of it")
	})
	return altered
}

// writeFormat puts the given format where the state of the channel is, as a
// database of another version of this store would have it. An empty format is
// the one of a version that wrote none at all, which is no format either.
func writeFormat(t testing.TB, dbPath string, channel string, format string) {
	t.Helper()

	inDatabase(t, dbPath, channel, func(handle db.DBHandle) {
		if format == "" {
			require.NoError(t, handle.Delete(metaKey(formatKey), true))
			return
		}
		require.NoError(t, handle.Put(metaKey(formatKey), []byte(format), true))
	})
}

// readFormat returns the format the state of the channel is of.
func readFormat(t testing.TB, dbPath string, channel string) string {
	t.Helper()

	var format string
	inDatabase(t, dbPath, channel, func(handle db.DBHandle) {
		blob, err := handle.Get(metaKey(formatKey))
		require.NoError(t, err)
		format = string(blob)
	})
	return format
}

// inDatabase runs the given function over the handle the state of the channel is
// kept in, and closes the database it belongs to afterwards.
func inDatabase(t testing.TB, dbPath string, channel string, work func(handle db.DBHandle)) {
	t.Helper()

	provider := providerAt(t, dbPath, &Conf{}, nil)
	defer provider.Close()
	work(provider.dbProvider.GetDBHandle(channel))
}
