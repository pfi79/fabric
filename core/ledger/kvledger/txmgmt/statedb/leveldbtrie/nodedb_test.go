/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package leveldbtrie

import (
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/triedb/hashdb"
	db "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/common/ledger/dataformat"
	"github.com/hyperledger/fabric/common/ledger/util/dbfactory"
	"github.com/stretchr/testify/require"
)

// channelName is the database the nodes of one channel are kept in.
const channelName = "ledger"

// emptyRoot is the root of a merkle tree with no entries, the value every
// implementation of the tree agrees on.
const emptyRoot = "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"

// TestTreeSurvivesAReopen commits a tree into a database, closes the database
// and opens it again. The root has to be the same, and every key has to be found
// again: the root alone proves nothing about the branches under it.
func TestTreeSurvivesAReopen(t *testing.T) {
	dbPath := t.TempDir()
	keys, values := entries(200)

	root := commitTo(t, dbPath, keys, values)

	// The database the helper committed to has been closed; this is the reopen.
	provider := openProvider(t, dbPath)
	defer provider.Close()

	tree, err := trie.New(trie.TrieID(root), NewNodeDB(provider.GetDBHandle(channelName)))
	require.NoError(t, err)
	require.Equal(t, root, tree.Hash())

	require.Equal(t, asMap(keys, values), readAll(t, tree))
}

// TestRootsMatchTheTreeOverGethsOwnStore commits the same entries twice, once
// into the database of this package and once into a store go-ethereum builds and
// addresses itself. The two roots have to be one root, otherwise what a channel
// reads back after a restart is not the tree that was committed to it.
func TestRootsMatchTheTreeOverGethsOwnStore(t *testing.T) {
	keys, values := entries(200)

	root := commitTo(t, t.TempDir(), keys, values)

	require.Equal(t, referenceRoot(t, keys, values), root)
}

// referenceRoot returns the root the tree of the given entries has in a store
// of go-ethereum's own making.
func referenceRoot(t *testing.T, keys, values [][]byte) common.Hash {
	t.Helper()

	store := hashdb.New(rawdb.NewMemoryDatabase(), nil)
	tree := trie.NewEmpty(store)
	require.NoError(t, tree.UpdateBatch(keys, values))

	root, nodes := tree.Commit(true)

	// go-ethereum links every leaf of the account trie to the storage trie the
	// leaf names, and decodes the leaf as a state account to do it. The leaves of
	// a plain trie are values and name nothing, so the reference goes in without
	// them.
	require.NoError(t, store.Update(root, types.EmptyRootHash, 0,
		trienode.NewWithNodeSet(&trienode.NodeSet{Owner: nodes.Owner, Nodes: nodes.Nodes})))

	opened, err := trie.New(trie.TrieID(root), store)
	require.NoError(t, err)
	return opened.Hash()
}

// TestEmptyTreeHasTheRootOfATreeWithNoEntries commits a tree that holds nothing
// and opens it again. Its root is the one fixed for a tree with no entries, and
// nothing is left in the database, there being no node to address.
func TestEmptyTreeHasTheRootOfATreeWithNoEntries(t *testing.T) {
	provider := openProvider(t, t.TempDir())
	defer provider.Close()

	handle := provider.GetDBHandle(channelName)
	nodeDB := NewNodeDB(handle)

	root, nodes := trie.NewEmpty(nodeDB).Commit(true)
	require.NoError(t, nodeDB.Commit(nodes))
	require.Equal(t, emptyRoot, root.Hex())

	opened, err := trie.New(trie.TrieID(root), nodeDB)
	require.NoError(t, err)
	require.Equal(t, root, opened.Hash())

	empty, err := handle.IsEmpty()
	require.NoError(t, err)
	require.True(t, empty, "a tree with no entries has no node to keep")
}

// TestOpeningOnARootTheDatabaseDoesNotHoldFails takes the node of the root away
// and opens the channel on that root again. The loss has to be reported, and the
// report has to name the root. A reader that answered nothing instead would
// present a lost tree as an empty state, and an empty state is a valid state
// that nothing downstream would be able to tell apart from it.
func TestOpeningOnARootTheDatabaseDoesNotHoldFails(t *testing.T) {
	dbPath := t.TempDir()
	keys, values := entries(200)
	root := commitTo(t, dbPath, keys, values)

	provider := openProvider(t, dbPath)
	defer provider.Close()

	handle := provider.GetDBHandle(channelName)
	require.NoError(t, handle.Delete(nodeKey(root[:]), true))

	tree, err := trie.New(trie.TrieID(root), NewNodeDB(handle))
	require.Error(t, err)
	require.Nil(t, tree)
	require.Contains(t, err.Error(), root.Hex())
}

// commitTo builds a tree over the given entries, writes its nodes into the
// database at dbPath, closes the database and returns the committed root.
func commitTo(t *testing.T, dbPath string, keys, values [][]byte) common.Hash {
	t.Helper()

	provider := openProvider(t, dbPath)
	defer provider.Close()

	nodeDB := NewNodeDB(provider.GetDBHandle(channelName))
	tree := trie.NewEmpty(nodeDB)
	require.NoError(t, tree.UpdateBatch(keys, values))

	// The leaves are collected along with the branches, the way a version of a
	// tree is committed rather than merely hashed.
	root, nodes := tree.Commit(true)
	require.NoError(t, nodeDB.Commit(nodes))
	return root
}

// readAll returns every key value pair the tree holds.
func readAll(t *testing.T, tree *trie.Trie) map[string]string {
	t.Helper()

	nodeIt, err := tree.NodeIterator(nil)
	require.NoError(t, err)

	found := map[string]string{}
	it := trie.NewIterator(nodeIt)
	for it.Next() {
		found[string(it.Key)] = string(it.Value)
	}
	require.NoError(t, it.Err)
	return found
}

// entries returns n key value pairs with keys in byte order and values too long
// to be folded into their parent, so that every leaf is a node of its own and
// has to come back out of the database.
func entries(n int) (keys [][]byte, values [][]byte) {
	for i := range n {
		keys = append(keys, []byte(fmt.Sprintf("key-%06d", i)))
		values = append(values, []byte(fmt.Sprintf("value-%06d-%s", i, "0123456789abcdef0123456789abcdef0123456789")))
	}
	return keys, values
}

func asMap(keys, values [][]byte) map[string]string {
	entries := map[string]string{}
	for i, key := range keys {
		entries[string(key)] = string(values[i])
	}
	return entries
}

func openProvider(t *testing.T, dbPath string) db.Provider {
	t.Helper()

	provider, err := dbfactory.NewProvider(db.GoLevelDB, dbPath, dataformat.CurrentFormat)
	require.NoError(t, err)
	return provider
}
