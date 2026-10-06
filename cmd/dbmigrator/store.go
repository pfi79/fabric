/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// The store of the world state that is being migrated is read and never written
// to. A LevelDB opened for writing rewrites what it holds — its journal is moved
// into a table of its own and its manifest is written down again — so the
// database of an operator is never opened at all: it is copied aside, and what
// is read is the copy, which is opened for writing. A trie is read out of the
// same kind of copy through the store of the trie, because a trie cannot be read
// at all out of a database that is not open for writing.
package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	db "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/common/ledger/util/dbfactory"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/statekvdb"
	"google.golang.org/protobuf/proto"
)

// internalDBName is the name of the logical database in which a provider keeps
// what it has to say about the database itself, the format of it among the rest.
// A channel is never called that, and what a provider has to say about a
// database is not a part of the state of a channel.
const internalDBName = "_"

// The keys the plain store keeps the state of a channel under: the marker of the
// data, the namespace, the separator between the namespace and the key, the key.
// The savepoint of the channel is kept under a key of its own, in the same
// channel.
var (
	plainDataPrefix = []byte{'d'}
	plainDataStop   = []byte{'e'}
	plainSavePoint  = []byte{'s'}
	namespaceSep    = byte(0x00)
)

// The keys the trie store keeps what a channel is committed at under: all of
// them under one prefix, and each of them under a name of its own under that
// prefix. This is the layout a reader of such a database is expected to know, and
// the migrator is such a reader. The number of the format is the one number this
// store reads, and the one number this migrator both reads and writes.
const (
	trieMetaPrefix    = 'm'
	trieFormatName    = 'f'
	trieRootName      = 'r'
	trieSavePointName = 'm'
)

// trieFormatNumber is the number of the format of a database the trie store
// keeps the state of a channel in. A database that names another number was
// written by another version of the store, its state is somewhere this version
// does not look, and reading it as if it were here would hand out a state the
// channel was never committed to.
var trieFormatNumber = []byte("1")

// stateFormat is the shape one of the two stores keeps the state of a channel
// in. The names are the ones each of the stores is known by: the first is the
// value the configuration of the peer names it under, the second the value the
// factory of databases takes.
type stateFormat int

const (
	formatPlain stateFormat = iota
	formatTrie
)

func (f stateFormat) String() string {
	if f == formatTrie {
		return "leveldbtrie"
	}
	return "goleveldb"
}

// parseFormat returns the format the given name is the value of a flag for.
// There are exactly two of them, and a name that is neither is refused while the
// command line is still being read rather than read as one of the two.
func parseFormat(name string) (stateFormat, error) {
	switch name {
	case formatTrie.String():
		return formatTrie, nil
	case formatPlain.String():
		return formatPlain, nil
	default:
		return 0, fmt.Errorf("--to must be either %s or %s, got [%s]", formatTrie, formatPlain, name)
	}
}

// trieMetaKey returns the key the piece of the metadata of the given name of a
// channel is kept under, before the name of the channel is put in front of it.
func trieMetaKey(name byte) []byte {
	return []byte{trieMetaPrefix, name}
}

// channelKey returns the key as it lies in the database: the name of the
// channel, the separator, and the key of the store itself.
func channelKey(channel string, key []byte) []byte {
	k := make([]byte, 0, len(channel)+1+len(key))
	k = append(k, channel...)
	k = append(k, namespaceSep)
	return append(k, key...)
}

// plainDB is a database of state as a run of the migrator reads it: the database
// an operator named is never opened, and what is opened here is a copy of it or a
// database this program has just written.
type plainDB struct {
	// path is the database as the operator named it, which is what the messages
	// of a run have to name rather than the copy that is read.
	path string
	db   db.DB
}

// openPlainDB opens the database at the given path for reading, through the copy
// of it at the given destination. What is opened is a database of the migrator's
// own — a copy of the one an operator named, or one this program has just
// written — and a database of LevelDB can only be read once it is open for
// writing. A database that is not there is an error rather than an empty
// database: this is a database an operator named, and a name that names nothing
// is not a database with nothing in it.
func openPlainDB(path, destination string) (handle *plainDB, err error) {
	dbHandle := dbfactory.CreateDB(dbType, destination, "")
	// The driver panics rather than returns where it cannot open a database, and
	// a database that cannot be opened is to be reported rather than allowed to
	// bring the run down.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("cannot open the database at [%s]: %v", path, r)
		}
	}()
	dbHandle.Open()
	return &plainDB{path: path, db: dbHandle}, nil
}

func (s *plainDB) Close() {
	s.db.Close()
}

// channels returns the names of the logical databases the given database holds
// state for, in the order of the names. A database of state keeps every channel
// in one database under the name of the channel, so the names of the channels
// are read out of the keys themselves rather than asked of the database: there
// is nothing else in the database that says what a channel is called.
func (s *plainDB) channels() ([]string, error) {
	itr, err := s.db.GetIterator(nil, nil)
	if err != nil {
		return nil, err
	}
	defer itr.Release()

	found := map[string]struct{}{}
	for itr.Next() {
		name, _, ok := strings.Cut(string(itr.Key()), string(namespaceSep))
		if !ok || name == internalDBName {
			continue
		}
		found[name] = struct{}{}
	}
	if err := itr.Error(); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// holds tells whether the database has any key of the given channel at all. A
// channel a database holds nothing for is not a channel of it, and is not
// migrated into a database that would then hold what nobody asked for.
func (s *plainDB) holds(channel string) (bool, error) {
	itr, err := s.db.GetIterator(
		channelKey(channel, nil),
		channelKey(channel+"\x01", nil),
	)
	if err != nil {
		return false, err
	}
	defer itr.Release()
	found := itr.Next()
	return found, itr.Error()
}

// channelFormat returns the format the state of the given channel is kept in the
// given database in. A database that names no format keeps no trie in it: the
// trie store writes the number of its format into every database it commits to,
// and the plain store writes no such number anywhere.
func formatOf(handle db.DB, channel string) (stateFormat, error) {
	blob, err := handle.Get(channelKey(channel, trieMetaKey(trieFormatName)))
	if err != nil {
		return 0, err
	}
	// A key that is not there names no format, which is the plain store.
	if len(blob) == 0 {
		return formatPlain, nil
	}
	if bytes.Equal(blob, trieFormatNumber) {
		return formatTrie, nil
	}
	return 0, fmt.Errorf(
		"the state of channel [%s] is in a format this migrator cannot read: the database holds [%s] where the number of the format of the trie is kept, and only [%s] is a number this migrator knows",
		channel, blob, trieFormatNumber,
	)
}

// channelFormat returns the format the state of the given channel is kept in
// this database in.
func (s *plainDB) channelFormat(channel string) (stateFormat, error) {
	return formatOf(s.db, channel)
}

// raw returns what the database holds under the given key of the given channel,
// and nothing at all, rather than an error, where the key is not there.
func (s *plainDB) raw(channel string, key []byte) ([]byte, error) {
	return s.db.Get(channelKey(channel, key))
}

// root returns the root the given channel of the given database is committed at,
// as the whole of it, for a report to the operator. A channel that has never been
// committed to is at the root of a tree with no entries, which is the one root
// every implementation of the tree agrees on.
func rootAt(handle db.DB, channel string) (string, error) {
	blob, err := handle.Get(channelKey(channel, trieMetaKey(trieRootName)))
	if err != nil {
		return "", err
	}
	if len(blob) == 0 {
		return "", fmt.Errorf("the state of channel [%s] is not in a trie: the database holds no root for it", channel)
	}
	return "0x" + hexOf(blob), nil
}

// savePointOf returns the height the given bytes name, as a value that holds
// nothing else, so that the caller can hand the version inside it on to a store
// without this migrator naming a type it is not allowed to name. Bytes that name
// no height are reported rather than read as one. Bytes that are no bytes at all
// name no savepoint, and the version of the value that is returned stands for
// that.
//
// The bytes a savepoint is written as are the bytes the version of a value is
// written as, and the encoding of a value is the one function the two stores
// share. A value that says nothing but the version therefore decodes into the
// height the savepoint names, and into nothing else.
func savePointOf(blob []byte) (*statedb.VersionedValue, error) {
	if len(blob) == 0 {
		return &statedb.VersionedValue{}, nil
	}
	asValue, err := proto.Marshal(&statekvdb.DBValue{Version: blob})
	if err != nil {
		return nil, err
	}
	return statekvdb.DecodeValue(asValue)
}

func hexOf(blob []byte) string {
	return hex.EncodeToString(blob)
}

// channelReader hands out the whole state of one channel, in the byte order of
// <namespace, key>. Both of the stores hand it out in that order: the trie is
// an ordinary trie, ordered by the bytes of its own keys, and so a pair of
// readers can be walked beside one another and compared as they go, without
// either of them being held in memory whole.
type channelReader interface {
	Next() (*statedb.VersionedKV, error)
	Close()
}

// plainReader reads the state of a channel out of the copy of a database,
// through the driver itself: the keys of the plain store are read as they are
// laid down.
type plainReader struct {
	itr     db.Iterator
	channel string
	// prefix is how much of a key is the name of the channel, the separator and
	// the marker of the data, which is not part of any pair of the state.
	prefix int
}

func newPlainReader(handle db.DB, channel string) (*plainReader, error) {
	itr, err := handle.GetIterator(
		channelKey(channel, plainDataPrefix),
		channelKey(channel, plainDataStop),
	)
	if err != nil {
		return nil, err
	}
	return &plainReader{
		itr:     itr,
		channel: channel,
		prefix:  len(channel) + 1 + len(plainDataPrefix),
	}, nil
}

func (r *plainReader) Next() (*statedb.VersionedKV, error) {
	if r.itr.Next() {
		rawKey := r.itr.Key()
		rest := rawKey[r.prefix:]
		ns, key, ok := strings.Cut(string(rest), string(namespaceSep))
		if !ok {
			return nil, fmt.Errorf("the key %#x of channel [%s] names no key in a namespace", rawKey, r.channel)
		}
		// The bytes of a value are only good until the reader is moved on, and
		// the value that comes out of the decoding may well be a view of them.
		blob := append([]byte(nil), r.itr.Value()...)
		vv, err := statekvdb.DecodeValue(blob)
		if err != nil {
			return nil, fmt.Errorf("cannot read what the database of channel [%s] holds under %#x: %w", r.channel, rawKey, err)
		}
		return &statedb.VersionedKV{
			CompositeKey:   &statedb.CompositeKey{Namespace: ns, Key: key},
			VersionedValue: vv,
		}, nil
	}
	if err := r.itr.Error(); err != nil {
		return nil, err
	}
	return nil, nil
}

func (r *plainReader) Close() {
	r.itr.Release()
}

// trieReader reads the state of a channel out of a trie, through the store of
// the trie itself.
type trieReader struct {
	itr statedb.FullScanIterator
}

func newTrieReader(itr statedb.FullScanIterator) *trieReader {
	return &trieReader{itr: itr}
}

func (r *trieReader) Next() (*statedb.VersionedKV, error) {
	return r.itr.Next()
}

func (r *trieReader) Close() {
	r.itr.Close()
}

// dbCopies are the copies of the databases a run reads. A database of an
// operator is never opened, because a LevelDB opened for writing rewrites what
// it holds, so what a run reads is always a copy of it. A copy is taken once,
// where the first reader asks for it, and is given back at the end of the run.
//
// A database that is read as a trie is given a copy of its own, because the
// store of the trie opens it for writing and holds it while it is read, and a
// database is held by the one who writes it. A database read through the driver
// itself is given a copy of its own for the same reason.
type dbCopies struct {
	root      string
	copies    int
	plainDirs map[string]string
	trieDirs  map[string]string
	plains    map[string]*plainDB
	providers map[string]statedb.VersionedDBProvider
}

func newDBCopies(root string) *dbCopies {
	return &dbCopies{
		root:      root,
		plainDirs: map[string]string{},
		trieDirs:  map[string]string{},
		plains:    map[string]*plainDB{},
		providers: map[string]statedb.VersionedDBProvider{},
	}
}

// copyAside copies the database at the given path into a directory of its own
// and returns that directory. A database that is not there is an error rather
// than an empty database: this is a database an operator named, and a name that
// names nothing is not a database with nothing in it.
func (c *dbCopies) copyAside(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("cannot open the database at [%s]: %w", path, err)
	}
	destination := fmt.Sprintf("%s.%d", c.root, c.copies)
	c.copies++
	if err := os.RemoveAll(destination); err != nil {
		return "", err
	}
	if err := copyDirectory(path, destination); err != nil {
		return "", fmt.Errorf("cannot copy the database at [%s] aside to read it: %w", path, err)
	}
	return destination, nil
}

// plain returns the database at the given path, read through the driver out of
// a copy of it.
func (c *dbCopies) plain(path string) (*plainDB, error) {
	if handle, ok := c.plains[path]; ok {
		return handle, nil
	}
	destination, ok := c.plainDirs[path]
	if !ok {
		var err error
		destination, err = c.copyAside(path)
		if err != nil {
			return nil, err
		}
		c.plainDirs[path] = destination
	}
	handle, err := openPlainDB(path, destination)
	if err != nil {
		return nil, err
	}
	c.plains[path] = handle
	return handle, nil
}

// providerFrom returns a provider of stores that hold the state of the channels
// of the given database in a trie, over a copy of the database.
func (c *dbCopies) providerFrom(path string) (statedb.VersionedDBProvider, error) {
	if provider, ok := c.providers[path]; ok {
		return provider, nil
	}
	destination, ok := c.trieDirs[path]
	if !ok {
		var err error
		destination, err = c.copyAside(path)
		if err != nil {
			return nil, err
		}
		c.trieDirs[path] = destination
	}
	provider, err := newProvider(destination, formatTrie)
	if err != nil {
		return nil, err
	}
	c.providers[path] = provider
	return provider, nil
}

func (c *dbCopies) close() {
	for _, provider := range c.providers {
		provider.Close()
	}
	for _, handle := range c.plains {
		handle.Close()
	}
	for _, destination := range c.plainDirs {
		c.remove(destination)
	}
	for _, destination := range c.trieDirs {
		c.remove(destination)
	}
}

func (c *dbCopies) remove(destination string) {
	if err := os.RemoveAll(destination); err != nil {
		fmt.Fprintf(os.Stderr, "cannot remove the working copy at [%s]: %v\n", destination, err)
	}
}

// readerFor returns a reader over the whole state of the given channel, in the
// format the given database holds that channel in.
//
// The format is not checked here: the caller has established it already, from the
// database it named, and a copy that is read as a trie is read through the store
// of the trie without being opened as a channel, because opening it would walk
// the whole of the tree once for every read of it.
func (c *dbCopies) readerFor(path string, format stateFormat, channel string) (channelReader, error) {
	if format == formatPlain {
		handle, err := c.plain(path)
		if err != nil {
			return nil, err
		}
		return newPlainReader(handle.db, channel)
	}

	provider, err := c.providerFrom(path)
	if err != nil {
		return nil, err
	}
	store, err := provider.GetDBHandle(channel, nil)
	if err != nil {
		return nil, err
	}
	itr, err := store.GetFullScanIterator(func(string) bool { return false })
	if err != nil {
		return nil, err
	}
	return newTrieReader(itr), nil
}

// copyDirectory copies the given directory into a directory of its own, file by
// file and with the permissions of each of them. What is copied is a database
// and nothing else: it is read through the driver afterwards, and not opened by
// hand.
func copyDirectory(source, destination string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot copy [%s]: it is not a file of the database", path)
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// sizeOf returns how many bytes the given directory takes up on the disk, the
// files of the database in it among them. It is the size of a database as an
// operator would count it, and the size a migration of it has to ask room for.
func sizeOf(path string) (uint64, error) {
	var total uint64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += uint64(info.Size())
		}
		return nil
	})
	return total, err
}

// dbType is the value the factory of databases is given for a store whose nodes
// are kept in a LevelDB. Both of the stores in this migrator are of that kind.
const dbType = db.GoLevelDB
