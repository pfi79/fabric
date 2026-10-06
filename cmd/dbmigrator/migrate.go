/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	db "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/common/ledger/util/dbfactory"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/leveldbtrie"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/statekvdb"
)

const (
	// defaultBatchSize is how much of a state is written as one version of the
	// store it is written to. It is the size the stores themselves write a
	// snapshot in, and the size the peak of the memory of a migration is set by.
	defaultBatchSize = 4 * 1024 * 1024

	// roomFactor is how much room a migration asks for in proportion to the size
	// of the database it migrates. The database the result is written into holds
	// the values a second time; a trie holds the nodes that hold them beside
	// them; and every database a run reads is copied aside first, because the
	// database of an operator is never opened. Three times the size of the
	// source is what all of that comes to.
	roomFactor = 3

	// The names of the directories a migration works in. Every one of them is
	// beside the database that is being written, so that the directory a peer
	// reads is never the one a migration writes into.
	tempSuffix      = ".dbmigrator.tmp"
	displacedSuffix = ".dbmigrator.old"
	workSuffix      = ".dbmigrator.work"
)

// availableBytes is how much room is left on a filesystem. It is a variable so
// that a test can put the question to a migration without a filesystem of its
// own that has no room left on it.
var availableBytes = freeBytes

// migrateOptions is what a run of the migrate command was asked for.
type migrateOptions struct {
	sourcePath string
	targetPath string
	to         stateFormat
	channels   []string
	batchSize  int
	verify     bool
	fileLock   string
}

// channelResult is what a run of the migrate command has to say about one
// channel.
type channelResult struct {
	channel string
	keys    int
	root    string
	skipped bool
	format  stateFormat
	from    stateFormat
}

// migrate copies the world state of the channels named out of the database at
// the source into the database at the target, in the format that was asked for.
//
// The database at the source is copied aside and read out of the copy, and is
// never opened at any point of the run. The result is written into a directory
// beside the target and is named the target only after it has been checked, so
// that a run that is stopped half way through leaves the target whole.
func migrate(o migrateOptions) error {
	if err := validateMigrateOptions(o); err != nil {
		return err
	}

	lock, err := lockTheFile(o.fileLock)
	if err != nil {
		return err
	}
	defer lock.Unlock()

	copies := newDBCopies(o.targetPath + workSuffix)
	defer copies.close()

	source, err := copies.plain(o.sourcePath)
	if err != nil {
		return err
	}

	channels, err := channelsToMigrate(source, o.channels)
	if err != nil {
		return err
	}
	if err := checkRoomFor(o.sourcePath, o.targetPath); err != nil {
		return err
	}

	// What has already been migrated into the target is carried over rather than
	// migrated again, so a channel that is already in the target format is left as
	// it is and the rest of the channels are added beside it. This is what makes a
	// run for one channel of a database of many worth running.
	//
	// Which of the channels those are is found out while the target is still shut,
	// because a database that is open for writing cannot be opened for reading at
	// the same time.
	alreadyThere, err := channelsAlreadyMigrated(copies, o.targetPath, o.to, channels)
	if err != nil {
		return err
	}

	finished := o.targetPath + tempSuffix
	// What a run that was stopped before left behind is of no use to the run that
	// follows it, and stands in the way of it.
	if err := os.RemoveAll(finished); err != nil {
		return fmt.Errorf("cannot clear [%s] out of the way: %w", finished, err)
	}
	if err := carryOver(o.targetPath, finished); err != nil {
		return err
	}

	m := &migrator{options: o, source: source, copies: copies}
	results, failures, err := m.migrateChannels(channels, alreadyThere, o.to, finished)
	if err != nil {
		return err
	}
	if failures > 0 {
		return fmt.Errorf(
			"%d of the %d channels could not be migrated; the unfinished result was left at [%s], where the next run clears it out of the way",
			failures, len(channels), finished,
		)
	}
	if migrated(results) == 0 {
		// Nothing was added, so the database that is there is the one the result
		// would be, and it is left exactly where it is.
		reportChannels(results)
		fmt.Fprintf(os.Stderr, "nothing was written into [%s]: %d channels were already in the format that was asked for\n", o.targetPath, carried(results))
		return os.RemoveAll(finished)
	}

	// The target is named only now, with the whole of the result in it and checked
	// against what was migrated. Until this moment the directory that is there is
	// the one that was there before, whole.
	if err := swapIn(finished, o.targetPath); err != nil {
		return err
	}

	reportChannels(results)
	if carried(results) > 0 {
		fmt.Fprintf(os.Stderr, "%d channels were left as they were: the database already held their state in the format that was asked for\n", carried(results))
	}
	return nil
}

// validateMigrateOptions refuses what cannot be run at all, before anything is
// opened and before anything is written.
func validateMigrateOptions(o migrateOptions) error {
	if o.batchSize < 1 {
		return fmt.Errorf("--batch-size must be at least one byte, got %d", o.batchSize)
	}
	source, err := filepath.Abs(o.sourcePath)
	if err != nil {
		return err
	}
	target, err := filepath.Abs(o.targetPath)
	if err != nil {
		return err
	}
	if source == target {
		return fmt.Errorf(
			"the source [%s] and the target [%s] are one and the same database: a migration writes into a database of its own and names it the target only after it has been checked",
			o.sourcePath, o.targetPath,
		)
	}
	return nil
}

// channelsToMigrate returns the channels the run was asked for: the ones that
// were named, or every channel of the database where none was.
func channelsToMigrate(source *plainDB, named []string) ([]string, error) {
	if len(named) == 0 {
		channels, err := source.channels()
		if err != nil {
			return nil, err
		}
		if len(channels) == 0 {
			return nil, fmt.Errorf("the database at [%s] holds the state of no channel at all", source.path)
		}
		return channels, nil
	}

	for _, channel := range named {
		held, err := source.holds(channel)
		if err != nil {
			return nil, err
		}
		if !held {
			return nil, fmt.Errorf("the database at [%s] holds the state of no channel called [%s]", source.path, channel)
		}
	}
	return named, nil
}

// checkRoomFor refuses to begin where there is not the room the migration needs.
// The room is asked of the directory the result is written beside, which is the
// filesystem the whole of the run writes to.
func checkRoomFor(sourcePath, targetPath string) error {
	size, err := sizeOf(sourcePath)
	if err != nil {
		return fmt.Errorf("cannot measure the database at [%s]: %w", sourcePath, err)
	}
	carried, err := sizeOf(targetPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot measure the database at [%s]: %w", targetPath, err)
	}

	// What is asked for is the size of what the result is made of — the values a
	// second time, and beside them the nodes of a trie that holds them — and the
	// copy every database a run reads is taken aside as, and, over and above
	// that, twice whatever the database being written already holds: it is
	// carried over into the result, and it is read out of a copy of its own.
	needed := size*roomFactor + 2*carried
	free, known := availableBytes(targetPath)
	if !known {
		fmt.Fprintf(os.Stderr,
			"Warning: the room left on the filesystem of [%s] cannot be found out on this platform; the migration goes ahead without the check.\n",
			targetPath,
		)
		return nil
	}
	if free < needed {
		return fmt.Errorf(
			"there is not enough room beside [%s] to migrate the database: the database at [%s] takes up %d bytes and the database that is being written takes up %d, while a migration of the first asks for %d bytes of them and only %d are free",
			targetPath, sourcePath, size, carried, needed, free,
		)
	}
	return nil
}

// carried returns how many channels of a run were left as they were, because the
// database that is being written already held their state in the format that was
// asked for.
func carried(results []channelResult) int {
	return len(results) - migrated(results)
}

// channelsAlreadyMigrated returns, for each of the given channels, whether the
// database at the given path already holds the state of that channel in the format
// that was asked for, which is the work of an earlier run and is not to be done
// over. A path that is not there holds no channel at all.
func channelsAlreadyMigrated(copies *dbCopies, path string, to stateFormat, channels []string) (map[string]bool, error) {
	handle, err := copies.plain(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	already := map[string]bool{}
	for _, channel := range channels {
		held, err := handle.holds(channel)
		if err != nil {
			return nil, err
		}
		if !held {
			continue
		}
		format, err := handle.channelFormat(channel)
		if err != nil {
			return nil, err
		}
		already[channel] = format == to
	}
	return already, nil
}

// migrated returns how many channels of a run were written as against those that
// were left as they were, because the database that is being written already held
// their state in the format that was asked for.
func migrated(results []channelResult) int {
	count := 0
	for _, result := range results {
		if !result.skipped {
			count++
		}
	}
	return count
}

// carryOver copies what the database that is being written already holds into the
// directory the result is written into, so that the channels an earlier run
// migrated are not lost by this one. A database that is not there holds nothing
// and there is nothing to carry over.
func carryOver(target, finished string) error {
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := copyDirectory(target, finished); err != nil {
		return fmt.Errorf("cannot copy the state already migrated into [%s] aside: %w", target, err)
	}
	return nil
}

// migrator carries what a run of a command needs to know about the databases it
// moves the state between.
type migrator struct {
	options migrateOptions
	source  *plainDB
	copies  *dbCopies
}

// migrateChannels migrates every one of the given channels and says what came of
// each of them, in the format asked for, into the directory named by finished.
// The channels that alreadyThere names are left as they are. A channel that could
// not be migrated is reported as it is met and does not stop the ones after it:
// the rest of the state is worth having, and a run that stopped at the first
// failure would leave the operator with nothing at all.
//
// The channels are written first, with the store of the result open for writing,
// and are checked only once it has been closed: a database of LevelDB is held by
// the one who writes it, and nothing else may open it until it is given back.
func (m *migrator) migrateChannels(channels []string, alreadyThere map[string]bool, to stateFormat, finished string) ([]channelResult, int, error) {
	results := make([]channelResult, 0, len(channels))
	failures := 0

	target, err := newProvider(finished, to)
	if err != nil {
		return nil, 0, err
	}
	for _, channel := range channels {
		if alreadyThere[channel] {
			results = append(results, channelResult{channel: channel, skipped: true, format: to})
			continue
		}
		result, err := m.writeChannel(target, to, channel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "channel [%s]: %v\n", channel, err)
			failures++
			continue
		}
		results = append(results, result)
	}
	target.Close()

	if m.options.verify {
		written, err := newProvider(finished, to)
		if err != nil {
			return results, failures, err
		}
		for i := range results {
			if results[i].skipped {
				continue
			}
			if err := m.checkState(written, results[i].channel, results[i].from); err != nil {
				fmt.Fprintf(os.Stderr, "channel [%s]: %v\n", results[i].channel, err)
				failures++
			}
		}
		written.Close()
	}

	if err := m.putRoots(results, to, finished); err != nil {
		return results, failures, err
	}
	return results, failures, nil
}

// writeChannel copies the whole state of one channel into the database being
// written and says what came of it. A channel that already stands in the format
// that was asked for is left as it is.
func (m *migrator) writeChannel(target statedb.VersionedDBProvider, to stateFormat, channel string) (channelResult, error) {
	from, err := m.source.channelFormat(channel)
	if err != nil {
		return channelResult{}, err
	}
	if from == to {
		return channelResult{channel: channel, skipped: true, format: to, from: from}, nil
	}

	reader, err := m.copies.readerFor(m.options.sourcePath, from, channel)
	if err != nil {
		return channelResult{}, err
	}
	defer reader.Close()

	savePoint, err := m.savePointOf(from, channel)
	if err != nil {
		return channelResult{}, err
	}

	store, err := target.GetDBHandle(channel, nil)
	if err != nil {
		return channelResult{}, err
	}
	keys, err := copyState(reader, store, savePoint, m.options.batchSize)
	if err != nil {
		return channelResult{}, err
	}
	return channelResult{channel: channel, keys: keys, format: to, from: from}, nil
}

// savePointOf returns the savepoint of the given channel as a value that holds
// nothing else, so that the height inside it can be handed on to the store it is
// written into without this migrator naming a type it is not allowed to name.
// A channel that was never committed to has no savepoint, and the version of the
// value that is returned stands for that.
func (m *migrator) savePointOf(from stateFormat, channel string) (*statedb.VersionedValue, error) {
	key := plainSavePoint
	if from == formatTrie {
		key = trieMetaKey(trieSavePointName)
	}
	blob, err := m.source.raw(channel, key)
	if err != nil {
		return nil, err
	}
	savePoint, err := savePointOf(blob)
	if err != nil {
		return nil, fmt.Errorf("the savepoint of this channel is not one this migrator can read: %w", err)
	}
	return savePoint, nil
}

// putRoots reads the root the state of every channel that was written is at.
// A state written into a trie names a root in the database that was written; a
// state written into the plain store names none, and the root of the trie it was
// migrated out of is the root of the same state and is what there is to report.
func (m *migrator) putRoots(results []channelResult, to stateFormat, finished string) error {
	if to == formatTrie {
		handle, err := openPlainDB(finished, finished)
		if err != nil {
			return err
		}
		defer handle.Close()
		for i := range results {
			if results[i].skipped {
				continue
			}
			root, err := rootAt(handle.db, results[i].channel)
			if err != nil {
				return err
			}
			results[i].root = root
		}
		return nil
	}

	for i := range results {
		if results[i].skipped || results[i].from != formatTrie {
			continue
		}
		root, err := rootAt(m.source.db, results[i].channel)
		if err != nil {
			return err
		}
		results[i].root = root
	}
	return nil
}

// checkState walks the state of the channel as the database that is being written
// holds it and the state as the database that was handed over holds it, beside
// one another, and reports the first pair that does not agree. Opening the store
// of the result is also what checks the tree as it stands and collects the nodes
// of the versions the batches of the migration left behind, which nothing reads.
func (m *migrator) checkState(written statedb.VersionedDBProvider, channel string, from stateFormat) error {
	want, err := m.copies.readerFor(m.options.sourcePath, from, channel)
	if err != nil {
		return err
	}
	defer want.Close()

	store, err := written.GetDBHandle(channel, nil)
	if err != nil {
		return err
	}
	if err := store.Open(); err != nil {
		return fmt.Errorf("the state that was written cannot be opened: %w", err)
	}
	itr, err := store.GetFullScanIterator(func(string) bool { return false })
	if err != nil {
		return err
	}
	got := newTrieReader(itr)
	defer got.Close()

	if _, err := compareState(channel, want, got); err != nil {
		return fmt.Errorf("the state that was written does not agree with the state that was migrated: %w", err)
	}
	return nil
}

// compareState walks two readers of the same state beside one another and returns
// how many pairs they agreed on, or the first pair of which they did not. The
// count of the keys of each is checked along the way: a reader that runs out
// while the other has more to hand is a pair that does not agree.
func compareState(channel string, want, got channelReader) (int, error) {
	agreed := 0
	for {
		expected, err := want.Next()
		if err != nil {
			return agreed, err
		}
		written, err := got.Next()
		if err != nil {
			return agreed, err
		}

		switch {
		case expected == nil && written == nil:
			return agreed, nil
		case expected == nil:
			return agreed, fmt.Errorf("channel [%s] holds [%s/%s], which the state that was migrated does not hold", channel, written.Namespace, written.Key)
		case written == nil:
			return agreed, fmt.Errorf("channel [%s] holds [%s/%s], which the state that was written does not hold", channel, expected.Namespace, expected.Key)
		}

		if err := comparePair(channel, expected, written); err != nil {
			return agreed, err
		}
		agreed++
	}
}

// comparePair compares one pair of the state as the bytes of it: the namespace,
// the key, the value, the metadata beside the value, and the version the value
// was written at. A pair that holds the same value and no version is not the
// state that was migrated.
func comparePair(channel string, expected, written *statedb.VersionedKV) error {
	at := fmt.Sprintf("channel [%s] at [%s/%s]", channel, expected.Namespace, expected.Key)
	switch {
	case expected.Namespace != written.Namespace:
		return fmt.Errorf("%s is in namespace [%s] in the state that was written", at, written.Namespace)
	case expected.Key != written.Key:
		return fmt.Errorf("%s is keyed [%s] in the state that was written", at, written.Key)
	case !bytes.Equal(expected.Value, written.Value):
		return fmt.Errorf("%s holds %d bytes of value where the state that was migrated holds %d", at, len(written.Value), len(expected.Value))
	case !bytes.Equal(expected.Metadata, written.Metadata):
		return fmt.Errorf("%s holds metadata that differs from the metadata of the state that was migrated", at)
	case !sameVersion(expected, written):
		return fmt.Errorf("%s is at version %s where the state that was migrated is at version %s", at, heightOf(written), heightOf(expected))
	}
	return nil
}

// sameVersion tells whether two versions name one and the same height, the
// heights compared as the bytes they are written as.
func sameVersion(left, right *statedb.VersionedKV) bool {
	if left.Version == nil || right.Version == nil {
		return left.Version == nil && right.Version == nil
	}
	return bytes.Equal(left.Version.ToBytes(), right.Version.ToBytes())
}

func heightOf(kv *statedb.VersionedKV) string {
	if kv.Version == nil {
		return "no version at all"
	}
	return kv.Version.String()
}

// copyState writes the whole of the given state into the given store, a batch at
// a time, and returns how many pairs it wrote.
//
// The last of the batches is the one committed with the savepoint of the channel,
// so that the height the state is consistent upto in the store it is written into
// is the height the state that was migrated was consistent upto. The batches
// before it are committed without one: a height written in the middle of a
// migration would be a height the state was never at.
func copyState(reader channelReader, store statedb.VersionedDB, savePoint *statedb.VersionedValue, batchSize int) (int, error) {
	batch := statedb.NewUpdateBatch()
	pending, written := 0, 0
	for {
		kv, err := reader.Next()
		if err != nil {
			return written, err
		}
		if kv == nil {
			break
		}
		batch.PutValAndMetadata(kv.Namespace, kv.Key, kv.Value, kv.Metadata, kv.Version)
		pending += len(kv.Key) + len(kv.Value) + len(kv.Metadata)
		written++
		if pending < batchSize {
			continue
		}
		if err := store.ApplyUpdates(batch, nil); err != nil {
			return written, err
		}
		batch, pending = statedb.NewUpdateBatch(), 0
	}
	if err := store.ApplyUpdates(batch, savePoint.Version); err != nil {
		return written, err
	}
	return written, nil
}

// newProvider returns the store of one of the two formats, over the database at
// the given path. Nothing here reads the configuration of a peer: an empty
// configuration of the trie store is the configuration of the specification,
// which is what a migration of a state into it wants — the tree is walked when a
// channel is opened, and the root of the version before the last one is kept
// beside the one the channel is at.
func newProvider(path string, format stateFormat) (statedb.VersionedDBProvider, error) {
	if format == formatTrie {
		return leveldbtrie.NewProvider(path, dbType, &leveldbtrie.Conf{}, nil)
	}
	return statekvdb.NewVersionedDBProvider(path, dbType)
}

// swapIn puts the finished directory where the database is to be read from and
// gives the room back afterwards. The directory that was there is moved out of
// the way first and removed only once the new one is in its place, so that a
// failure in between leaves the directory that was there rather than neither.
func swapIn(finished, target string) error {
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return os.Rename(finished, target)
	} else if err != nil {
		return err
	}

	displaced := target + displacedSuffix
	if err := os.RemoveAll(displaced); err != nil {
		return err
	}
	if err := os.Rename(target, displaced); err != nil {
		return err
	}
	if err := os.Rename(finished, target); err != nil {
		// What was there is put back rather than left to be found missing by
		// whatever the operator was about to start.
		if back := os.Rename(displaced, target); back != nil {
			return fmt.Errorf("%w, and the database that was at [%s] could not be put back either: %w", err, target, back)
		}
		return err
	}
	return os.RemoveAll(displaced)
}

// reportChannels says what came of every channel of a run: how many keys its
// state holds, and the whole of the root of the state, which a gauge of a metric
// cannot hold and an operator has to be able to write down.
func reportChannels(results []channelResult) {
	for _, result := range results {
		if result.skipped {
			fmt.Printf("channel [%s]: skipped, the database already holds its state in the format that was asked for (%s)\n", result.channel, result.format)
			continue
		}
		fmt.Printf("channel [%s]: %d keys, root %s\n", result.channel, result.keys, result.root)
	}
}

// purgeOptions is what a run of the purge command was asked for.
type purgeOptions struct {
	sourcePath string
	targetPath string
	fileLock   string
	confirm    bool
}

// purge removes the database that was migrated, once, and only once, the database
// it was migrated into has been found to hold the whole of its state. A run
// without --confirm says what it would remove and removes nothing.
func purge(o purgeOptions) error {
	lock, err := lockTheFile(o.fileLock)
	if err != nil {
		return err
	}
	defer lock.Unlock()

	copies := newDBCopies(o.targetPath + workSuffix)
	defer copies.close()

	source, err := copies.plain(o.sourcePath)
	if err != nil {
		return err
	}

	channels, err := channelsToMigrate(source, nil)
	if err != nil {
		return err
	}

	target, err := copies.plain(o.targetPath)
	if err != nil {
		return fmt.Errorf("the database the state was migrated into ([%s]) cannot be opened, so there is nothing to show that it holds the whole of the state: %w", o.targetPath, err)
	}

	m := &migrator{
		options: migrateOptions{sourcePath: o.sourcePath, targetPath: o.targetPath},
		source:  source,
		copies:  copies,
	}
	for _, channel := range channels {
		if err := m.checkPurgeable(target, channel); err != nil {
			return err
		}
	}

	if !o.confirm {
		fmt.Fprintf(os.Stderr,
			"the state of %d channels was found whole in both [%s] and [%s]; pass --confirm to remove [%s]\n",
			len(channels), o.sourcePath, o.targetPath, o.sourcePath,
		)
		return errors.New("nothing was removed: --confirm was not given")
	}

	size, err := sizeOf(o.sourcePath)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(o.sourcePath); err != nil {
		return fmt.Errorf("the state was found whole in [%s] and [%s], but [%s] could not be removed: %w", o.sourcePath, o.targetPath, o.sourcePath, err)
	}
	fmt.Printf("removed [%s] (%d bytes); the state of %d channels is in [%s]\n", o.sourcePath, size, len(channels), o.targetPath)
	return nil
}

// checkPurgeable finds out whether the state of one channel is in the database
// that was migrated into in whole, and refuses to go on where it is not: what a
// purge removes cannot be got back out of the state that was left behind.
func (m *migrator) checkPurgeable(target *plainDB, channel string) error {
	from, err := m.source.channelFormat(channel)
	if err != nil {
		return err
	}
	to, err := target.channelFormat(channel)
	if err != nil {
		return err
	}
	if from == to {
		return fmt.Errorf(
			"the state of channel [%s] is in the same format ([%s]) in both databases, so neither is a migration of the other and there is nothing to purge",
			channel, from,
		)
	}

	want, err := m.copies.readerFor(m.options.sourcePath, from, channel)
	if err != nil {
		return err
	}
	defer want.Close()

	got, err := m.copies.readerFor(m.options.targetPath, to, channel)
	if err != nil {
		return err
	}
	defer got.Close()

	if _, err := compareState(channel, want, got); err != nil {
		return fmt.Errorf("the database at [%s] does not hold the state of channel [%s] in whole, and nothing is removed: %w", m.options.targetPath, channel, err)
	}
	return nil
}

// lockTheFile takes the lock a peer takes on the same path, so that the two
// cannot be holding one database at once. A peer that is running holds it, and a
// migration refuses to begin rather than write beside a running one.
func lockTheFile(path string) (db.FileLock, error) {
	lock := dbfactory.NewFileLock(dbType, path)
	if err := lock.Lock(); err != nil {
		return nil, fmt.Errorf(
			"the lock on [%s] is held, which is what a running peer holds it with: stop the peer before migrating its state (%w)",
			path, err,
		)
	}
	return lock, nil
}
