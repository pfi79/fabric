/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package leveldbtrie

import (
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	db "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/commontests"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/statekvdb"
	"github.com/stretchr/testify/require"
)

// newTestEnv gives a provider over a database of this test's own making, the
// same way the level db store is given one.
func newTestEnv(t testing.TB) *versionedDBProvider {
	t.Helper()
	return newTestEnvWithConf(t, &Conf{}, nil)
}

// newTestEnvWithConf gives a provider that runs on the given configuration and
// reports what its stores commit to the given reporter.
func newTestEnvWithConf(t testing.TB, conf *Conf, reporter statedb.RootReporter) *versionedDBProvider {
	t.Helper()

	provider, err := NewProvider(t.TempDir(), db.GoLevelDB, conf, reporter)
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	return provider.(*versionedDBProvider)
}

// recordedRoots writes down the roots a store reports.
//
// The root of a version is not part of what a store hands back to whoever uses
// it: the reporter is how the root of a committed version comes out of the
// store at all, and so is how a test sees whether a root moved.
type recordedRoots struct {
	blockRoots  []rootOfBlock
	txRoots     []rootOfTx
	collections []collection
}

type rootOfBlock struct {
	blockNum uint64
	root     string
}

type rootOfTx struct {
	blockNum uint64
	txNum    uint64
	root     string
}

type collection struct {
	live      int
	reclaimed int
}

func (r *recordedRoots) BlockRoot(channel string, height uint64, root []byte) {
	r.blockRoots = append(r.blockRoots, rootOfBlock{height, common.BytesToHash(root).Hex()})
}

func (r *recordedRoots) TxRoot(channel string, height uint64, txNum uint64, root []byte) {
	r.txRoots = append(r.txRoots, rootOfTx{height, txNum, common.BytesToHash(root).Hex()})
}

func (r *recordedRoots) GC(channel string, liveNodes int, reclaimedNodes int, durationMillis float64) {
	r.collections = append(r.collections, collection{liveNodes, reclaimedNodes})
}

func TestBasicRW(t *testing.T) {
	commontests.TestBasicRW(t, newTestEnv(t))
}

func TestDeletes(t *testing.T) {
	commontests.TestDeletes(t, newTestEnv(t))
}

func TestIterator(t *testing.T) {
	commontests.TestIterator(t, newTestEnv(t))
}

func TestPaginatedRangeQuery(t *testing.T) {
	commontests.TestPaginatedRangeQuery(t, newTestEnv(t))
}

func TestRangeQuerySpecialCharacters(t *testing.T) {
	commontests.TestRangeQuerySpecialCharacters(t, newTestEnv(t))
}

func TestDataExportImport(t *testing.T) {
	// smaller batch size for testing to cover the boundary case of writing the final batch
	maxDataImportBatchSize = 10
	commontests.TestDataExportImport(t, newTestEnv(t))
}

func TestMultiDBBasicRW(t *testing.T) {
	commontests.TestMultiDBBasicRW(t, newTestEnv(t))
}

func TestGetStateMultipleKeys(t *testing.T) {
	commontests.TestGetStateMultipleKeys(t, newTestEnv(t))
}

func TestGetVersion(t *testing.T) {
	commontests.TestGetVersion(t, newTestEnv(t))
}

func TestValueAndMetadataWrites(t *testing.T) {
	commontests.TestValueAndMetadataWrites(t, newTestEnv(t))
}

func TestApplyUpdatesWithNilHeight(t *testing.T) {
	commontests.TestApplyUpdatesWithNilHeight(t, newTestEnv(t))
}

// TestQueryNotSupported puts state in the store and asks it a question it has
// no answer for. The answer has to be that it has none: an empty result would
// read as a question that was asked and came back with nothing, which is a
// different thing altogether.
func TestQueryNotSupported(t *testing.T) {
	provider := newTestEnv(t)
	vdb, err := provider.GetDBHandle("testquery", nil)
	require.NoError(t, err)
	require.NoError(t, vdb.Open())
	defer vdb.Close()

	batch := statedb.NewUpdateBatch()
	batch.Put("ns1", "key1", []byte(`{"asset_name": "marble1","color": "blue","size": 1,"owner": "tom"}`), version.NewHeight(1, 1))
	require.NoError(t, vdb.ApplyUpdates(batch, version.NewHeight(2, 22)))

	itr, err := vdb.ExecuteQuery("ns1", `{"selector":{"owner":"jerry"}}`)
	require.EqualError(t, err, "ExecuteQuery not supported for leveldbtrie")
	require.Nil(t, itr)

	itr, err = vdb.ExecuteQueryWithPagination("ns1", `{"selector":{"owner":"jerry"}}`, "", 10)
	require.EqualError(t, err, "ExecuteQueryWithMetadata not supported for leveldbtrie")
	require.Nil(t, itr)
}

// TestRangeQueryOrderMatchesTheLevelDBStore fills two databases the same way,
// one in each of the two stores, and walks both with a paginated range query.
// The two walks have to hand out the same keys in the same order, key by key
// and page by page, because the pagination of a range query is built on that
// order: a bookmark names a key, and the next page starts at it.
func TestRangeQueryOrderMatchesTheLevelDBStore(t *testing.T) {
	// The keys are written out rather than sorted here. A test that puts them
	// in order the way the store under test puts them in order cannot disagree
	// with it, and so cannot catch it handing them out in the wrong order. This
	// list is the byte order of the keys, read off by hand: key10 sits between
	// key1 and key2.
	expectedKeys := []string{"key1", "key10", "key2", "key3", "key4", "key5", "key6", "key7", "key8", "key9"}
	const pageSize = 3

	trieProvider := newTestEnv(t)
	kvProvider, err := statekvdb.NewVersionedDBProvider(t.TempDir(), db.GoLevelDB)
	require.NoError(t, err)
	defer kvProvider.Close()

	providers := map[string]statedb.VersionedDBProvider{
		"leveldbtrie": trieProvider,
		"statekvdb":   kvProvider,
	}

	found := map[string][]string{}
	for name, provider := range providers {
		t.Run(name, func(t *testing.T) {
			vdb, err := provider.GetDBHandle("rangequeryorder", nil)
			require.NoError(t, err)

			batch := statedb.NewUpdateBatch()
			for i := 1; i <= 10; i++ {
				batch.Put("ns", fmt.Sprintf("key%d", i), fmt.Appendf(nil, "value%d", i), version.NewHeight(1, uint64(i)))
			}
			require.NoError(t, vdb.ApplyUpdates(batch, version.NewHeight(1, 10)))

			var keys []string
			bookmark := ""
			// Every page hands out at least one key, so no walk can need more
			// pages than there are keys, and a store that hands out a page of
			// nothing stops the walk here rather than for ever.
			for range expectedKeys {
				itr, err := vdb.GetStateRangeScanIteratorWithPagination("ns", bookmark, "", pageSize)
				require.NoError(t, err)
				for {
					kv, err := itr.Next()
					require.NoError(t, err)
					if kv == nil {
						break
					}
					keys = append(keys, kv.Key)
				}
				bookmark = itr.GetBookmarkAndClose()
				if bookmark == "" {
					break
				}
			}
			found[name] = keys
		})
	}

	require.Equal(t, found["statekvdb"], found["leveldbtrie"])
	require.Equal(t, expectedKeys, found["statekvdb"])
	require.Equal(t, expectedKeys, found["leveldbtrie"])
}

func TestDrop(t *testing.T) {
	provider := newTestEnv(t)

	// The drop is expected to empty the very database the state was written to,
	// so that is what is asked about afterwards.
	commontests.TestDrop(t, provider, func(channelName string) {
		empty, err := provider.dbProvider.GetDBHandle(channelName).IsEmpty()
		require.NoError(t, err)
		require.True(t, empty)
	})
}
