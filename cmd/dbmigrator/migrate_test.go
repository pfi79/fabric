/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	db "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/common/ledger/dataformat"
	"github.com/hyperledger/fabric/common/ledger/util/dbfactory"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/statekvdb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// statePair is one pair of a state a migration has to carry over whole.
type statePair struct {
	ns       string
	key      string
	value    []byte
	metadata []byte
	// version is the bytes a height is written as, which is the length of the
	// number that follows and the number itself, twice over: {BlockNum: 1,
	// TxNum: 0} is 01 01 00, {BlockNum: 2, TxNum: 5} is 01 02 01 05, and
	// {BlockNum: 12345, TxNum: 7} is 02 30 39 01 07. They are written out here
	// rather than made by the store under test, so that what a migration is
	// expected to keep was worked out by hand and not read back out of the code
	// that has to reproduce it.
	version []byte
}

// The state the round trip is run over. The keys are written out in the order of
// the bytes, and one of them is a key that sorts before another of them by more
// than one character, so that an order that is a sort of the names rather than of
// the bytes would be caught here and nowhere else.
var roundTripState = []statePair{
	{ns: "ns1", key: "key1", value: []byte("one"), metadata: []byte("md1"), version: []byte{0x01, 0x01, 0x00}},
	{ns: "ns1", key: "key10", value: []byte("ten"), metadata: nil, version: []byte{0x01, 0x01, 0x00}},
	{ns: "ns1", key: "key2", value: []byte("two"), metadata: nil, version: []byte{0x01, 0x02, 0x01, 0x05}},
	{ns: "ns2", key: "empty-value", value: []byte{}, metadata: []byte{0x00}, version: []byte{0x02, 0x30, 0x39, 0x01, 0x07}},
	{ns: "ns2", key: "with-a-0x00-in-the-key", value: []byte("sep"), metadata: nil, version: []byte{0x00, 0x00}},
}

// theSavePoint is the height a channel of the state above is consistent upto,
// {BlockNum: 7, TxNum: 3}.
var theSavePoint = []byte{0x01, 0x07, 0x01, 0x03}

// encoded returns what the pair is written as in a database of state.
func (p statePair) encoded(t *testing.T) []byte {
	t.Helper()
	blob, err := proto.Marshal(&statekvdb.DBValue{
		Version:  p.version,
		Value:    p.value,
		Metadata: p.metadata,
	})
	require.NoError(t, err)
	return blob
}

// storedKey returns the key the pair is written under inside the database of a
// channel: the marker of the data, the namespace, the separator, the key. It is
// written out here, and not asked of the migrator, so that a key laid down in one
// place and read back in another is the same key by construction and not by two
// pieces of code agreeing with one another.
func (p statePair) storedKey() []byte {
	key := []byte{'d'}
	key = append(key, p.ns...)
	key = append(key, 0x00)
	return append(key, p.key...)
}

// writePlainState fills a database of state in the plain format by hand, the way
// the peer that has one writes it.
func writePlainState(t *testing.T, path, channel string, pairs []statePair, savePoint []byte) {
	t.Helper()

	provider, err := dbfactory.NewProvider(db.GoLevelDB, path, dataformat.CurrentFormat)
	require.NoError(t, err)
	defer provider.Close()

	handle := provider.GetDBHandle(channel)
	batch := handle.NewUpdateBatch()
	for _, pair := range pairs {
		batch.Put(pair.storedKey(), pair.encoded(t))
	}
	if savePoint != nil {
		batch.Put([]byte{'s'}, savePoint)
	}
	require.NoError(t, handle.WriteBatch(batch, true))
}

// readPlainState returns what the database holds under the keys of the state of
// the given channel, and the savepoint of the channel, exactly as the bytes are
// laid down. It reads the database through the driver itself rather than through
// anything of this migrator, so that what a migration wrote is compared with what
// is on the disk and not with what this program makes of it.
func readPlainState(t *testing.T, path, channel string) (map[string][]byte, []byte) {
	t.Helper()

	handle := dbfactory.CreateDB(db.GoLevelDB, path, "")
	handle.Open()
	defer handle.Close()

	prefix := append([]byte(channel), 0x00)
	pairs := map[string][]byte{}
	itr, err := handle.GetIterator(
		append(append([]byte{}, prefix...), 'd'),
		append(append([]byte{}, prefix...), 'e'),
	)
	require.NoError(t, err)
	for itr.Next() {
		pairs[string(itr.Key()[len(prefix)+1:])] = append([]byte{}, itr.Value()...)
	}
	require.NoError(t, itr.Error())
	itr.Release()

	savePoint, err := handle.Get(append(append([]byte{}, prefix...), 's'))
	require.NoError(t, err)
	return pairs, savePoint
}

// expectedPairs returns what the state above is expected to be on the disk of a
// database of state, keyed by the bytes of the key inside the channel.
func expectedPairs(t *testing.T) map[string][]byte {
	t.Helper()
	expected := map[string][]byte{}
	for _, pair := range roundTripState {
		stored := pair.storedKey()
		expected[string(stored[1:])] = pair.encoded(t)
	}
	return expected
}

// optionsFor returns what a run of the migrate command is asked for over the two
// directories of a test, with the lock of the peer taken where a peer takes it.
func optionsFor(source, target string, to stateFormat) migrateOptions {
	return migrateOptions{
		sourcePath: source,
		targetPath: target,
		to:         to,
		batchSize:  defaultBatchSize,
		verify:     true,
		fileLock:   filepath.Join(filepath.Dir(source), "fileLock"),
	}
}

func TestTheWholeStateIsCarriedOverBothWaysByteForByte(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "stateLeveldb")

	writePlainState(t, plain, "mychannel", roundTripState, theSavePoint)

	// Out of the flat store and into the trie.
	require.NoError(t, migrate(optionsFor(plain, filepath.Join(root, "trie"), formatTrie)))

	// And back out of the trie into a flat store of its own. What comes out is
	// compared with what went in, byte by byte, and not with what the store that
	// read it says about it.
	backAgain := filepath.Join(root, "stateLeveldb.again")
	require.NoError(t, migrate(optionsFor(filepath.Join(root, "trie"), backAgain, formatPlain)))

	pairs, savePoint := readPlainState(t, backAgain, "mychannel")
	require.Equal(t, expectedPairs(t), pairs)
	require.Equal(t, theSavePoint, savePoint, "the height the state is consistent upto was not carried over")

	// And once more into a trie, to show that the state that went back is the
	// state that came out of the first trie and can be read as one.
	trieAgain := filepath.Join(root, "trie.again")
	require.NoError(t, migrate(optionsFor(backAgain, trieAgain, formatTrie)))
	readTrieState(t, trieAgain, "mychannel")
}

// readTrieState reads the whole state of a channel out of a database in the trie
// format, through the store of the trie, and compares it with the state above
// one pair at a time.
func readTrieState(t *testing.T, path, channel string) {
	t.Helper()

	provider, err := newProvider(path, formatTrie)
	require.NoError(t, err)
	defer provider.Close()

	store, err := provider.GetDBHandle(channel, nil)
	require.NoError(t, err)
	require.NoError(t, store.Open())

	itr, err := store.GetFullScanIterator(func(string) bool { return false })
	require.NoError(t, err)
	defer itr.Close()

	agreed := 0
	for _, expected := range roundTripState {
		written, err := itr.Next()
		require.NoError(t, err)
		require.NotNilf(t, written, "the state ends before %s/%s", expected.ns, expected.key)
		require.Equal(t, expected.ns, written.Namespace)
		require.Equal(t, expected.key, written.Key)
		require.Equal(t, expected.value, written.Value)
		require.Equal(t, expected.metadata, written.Metadata)
		require.Equal(t, expected.version, written.Version.ToBytes())
		agreed++
	}
	last, err := itr.Next()
	require.NoError(t, err)
	require.Nil(t, last, "the state holds more pairs than the state that was migrated")
	require.Equal(t, len(roundTripState), agreed)

	savePoint, err := store.GetLatestSavePoint()
	require.NoError(t, err)
	require.Equal(t, theSavePoint, savePoint.ToBytes())
}

// snapshotOf reads every file of a directory and returns what each of them is:
// how many bytes it holds and a digest of them. It is taken before and after a
// migration to show that the database a migration was handed does not change.
func snapshotOf(t *testing.T, dir string) map[string]string {
	t.Helper()

	files := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(content)
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[relative] = fmt.Sprintf("%d/%s", len(content), hex.EncodeToString(digest[:]))
		return nil
	})
	require.NoError(t, err)
	return files
}

func TestTheSourceIsLeftUntouchedWhicheverWayTheStateGoes(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "stateLeveldb")
	writePlainState(t, plain, "mychannel", roundTripState, theSavePoint)

	beforePlain := snapshotOf(t, plain)
	trie := filepath.Join(root, "trie")
	require.NoError(t, migrate(optionsFor(plain, trie, formatTrie)))
	require.Equal(t, beforePlain, snapshotOf(t, plain), "a migration out of the plain store wrote to it")

	beforeTrie := snapshotOf(t, trie)
	backAgain := filepath.Join(root, "stateLeveldb.again")
	require.NoError(t, migrate(optionsFor(trie, backAgain, formatPlain)))
	require.Equal(t, beforeTrie, snapshotOf(t, trie), "a migration out of the trie wrote to it")
}

func TestAChannelAlreadyInTheTargetFormatIsSkipped(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "stateLeveldb")
	writePlainState(t, plain, "mychannel", roundTripState, theSavePoint)

	trie := filepath.Join(root, "trie")
	require.NoError(t, migrate(optionsFor(plain, trie, formatTrie)))

	// A second run over the same target has nothing to do: the state of the
	// channel is in the format that is asked for already, and what is there is
	// left as it is.
	require.NoError(t, migrate(optionsFor(plain, trie, formatTrie)))
	readTrieState(t, trie, "mychannel")
}

func TestAMigrationRefusesWhileAPeerHoldsTheLock(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "stateLeveldb")
	writePlainState(t, plain, "mychannel", roundTripState, theSavePoint)

	lock := dbfactory.NewFileLock(db.GoLevelDB, filepath.Join(root, "fileLock"))
	require.NoError(t, lock.Lock())
	defer lock.Unlock()

	err := migrate(optionsFor(plain, filepath.Join(root, "trie"), formatTrie))
	require.Error(t, err)
	require.Contains(t, err.Error(), "is held")
}

func TestAMigrationRefusesWhenThereIsNoRoom(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "stateLeveldb")
	writePlainState(t, plain, "mychannel", roundTripState, theSavePoint)

	room := availableBytes
	availableBytes = func(string) (uint64, bool) { return 0, true }
	t.Cleanup(func() { availableBytes = room })

	err := migrate(optionsFor(plain, filepath.Join(root, "trie"), formatTrie))
	require.Error(t, err)
	require.Contains(t, err.Error(), "not enough room")
}

// TestTheStateIsCarriedOverInBatchesSmallerThanIt shows that a state larger than
// the batch it is carried over in still comes out whole, both ways: what bounds a
// migration is the batch it was given and not the size of the database.
func TestTheStateIsCarriedOverInBatchesSmallerThanIt(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "stateLeveldb")

	pairs := make([]statePair, 0, 500)
	for i := range 500 {
		pairs = append(pairs, statePair{
			ns:      "ns",
			key:     fmt.Sprintf("key-%04d", i),
			value:   []byte(strings.Repeat("v", 40)),
			version: []byte{0x01, 0x02, 0x00},
		})
	}
	writePlainState(t, plain, "mychannel", pairs, theSavePoint)

	trie := filepath.Join(root, "trie")
	options := optionsFor(plain, trie, formatTrie)
	options.batchSize = 128
	require.NoError(t, migrate(options))

	backAgain := filepath.Join(root, "stateLeveldb.again")
	options = optionsFor(trie, backAgain, formatPlain)
	options.batchSize = 128
	require.NoError(t, migrate(options))

	expected := map[string][]byte{}
	for _, pair := range pairs {
		expected[string(pair.storedKey()[1:])] = pair.encoded(t)
	}
	got, savePoint := readPlainState(t, backAgain, "mychannel")
	require.Equal(t, theSavePoint, savePoint)
	require.Equal(t, expected, got)
}

func TestALeftoverWorkingDirectoryIsClearedBeforeTheRun(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "stateLeveldb")
	writePlainState(t, plain, "mychannel", roundTripState, theSavePoint)

	target := filepath.Join(root, "trie")
	leftover := target + tempSuffix
	require.NoError(t, os.MkdirAll(leftover, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(leftover, "stale"), []byte("junk"), 0o644))

	require.NoError(t, migrate(optionsFor(plain, target, formatTrie)))
	readTrieState(t, target, "mychannel")
}
