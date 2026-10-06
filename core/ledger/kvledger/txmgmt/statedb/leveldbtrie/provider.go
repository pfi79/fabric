/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package leveldbtrie

import (
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	db "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/common/ledger/dataformat"
	"github.com/hyperledger/fabric/common/ledger/util/dbfactory"
	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
)

var logger = flogging.MustGetLogger("leveldbtrie")

// defaultKeepRoots is how many roots of past versions a store writes down beside
// the one it is committed at when its configuration says nothing about it: the
// root the channel is at and the root it was at before it.
const defaultKeepRoots = 2

// Conf is what the configuration of this store says. Every key of it is
// declared here so that the configuration can name the whole of it at once; the
// keys themselves are read by the task that wires this store into the peer.
//
// A Conf that says nothing at all is the configuration of the specification:
// VerifyOnOpen is on, and KeepRoots is the root the channel is at beside the one
// it was at before it. A configuration that says anything at all is taken as it
// stands.
type Conf struct {
	// VerifyOnOpen makes the store walk its tree when it is opened, to find out
	// whether what the nodes of the tree are said to be agrees with the tree
	// itself. It is on unless the configuration says so.
	VerifyOnOpen bool
	// KeepRoots is the number of roots of past versions the store writes down
	// along with the one it is committed at. It is at least one, a root with no
	// nodes under it reads as an empty state.
	KeepRoots int
	// GCIntervalBlocks is the number of blocks between two runs of the collector
	// of the nodes no version reaches any more.
	GCIntervalBlocks int
	// ExactMetrics makes the metrics report the whole root rather than the
	// number that fits a gauge.
	ExactMetrics bool
}

// versionedDBProvider holds the world state of the channels of a peer in tries
// whose nodes are kept in the LevelDB of the channel.
type versionedDBProvider struct {
	dbProvider db.Provider
	dbPath     string
	conf       *Conf
	reporter   statedb.RootReporter
}

// NewProvider returns a provider of stores that hold the world state of a
// channel in a patricia merkle trie.
func NewProvider(dbPath string, dbType string, conf *Conf, reporter statedb.RootReporter) (statedb.VersionedDBProvider, error) {
	logger.Debugf("constructing VersionedDBProvider dbPath=%s", dbPath)
	dbProvider, err := dbfactory.NewProvider(dbType, dbPath, dataformat.CurrentFormat)
	if err != nil {
		return nil, err
	}
	return &versionedDBProvider{dbProvider: dbProvider, dbPath: dbPath, conf: withDefaults(conf), reporter: reporter}, nil
}

// withDefaults returns the configuration the stores of this provider are to run
// on. A configuration that names no key at all is read as the one the
// specification gives: the tree is walked when a channel is opened, the root of
// the version before the last one is written down beside the one the channel is
// at, and the garbage of the versions behind is collected when a channel is
// opened.
//
// VerifyOnOpen is a plain bool and so cannot be told from a key that was left
// out. A configuration that names any key of itself is read as a whole: one that
// is named with the walk off has the walk off, and only one that says nothing
// gets the walk the specification gives it. A peer reads the key out of its own
// configuration, which gives it a value either way.
func withDefaults(conf *Conf) *Conf {
	if conf == nil || *conf == (Conf{}) {
		return &Conf{VerifyOnOpen: true, KeepRoots: defaultKeepRoots}
	}
	resolved := *conf
	if resolved.KeepRoots < 1 {
		resolved.KeepRoots = defaultKeepRoots
	}
	if resolved.GCIntervalBlocks < 0 {
		resolved.GCIntervalBlocks = 0
	}
	return &resolved
}

// GetDBHandle returns a handle to the state of the given channel.
func (provider *versionedDBProvider) GetDBHandle(dbName string, namespaceProvider statedb.NamespaceProvider) (statedb.VersionedDB, error) {
	return newVersionedDB(provider.dbProvider.GetDBHandle(dbName), dbName, provider.dbPath, provider.conf, provider.reporter), nil
}

// ImportFromSnapshot loads the state from the snapshot files previously generated
// by using the FullScanIterator.
func (provider *versionedDBProvider) ImportFromSnapshot(
	dbName string,
	savepoint *version.Height,
	itr statedb.FullScanIterator,
) error {
	vdb := newVersionedDB(provider.dbProvider.GetDBHandle(dbName), dbName, provider.dbPath, provider.conf, provider.reporter)
	return vdb.importState(itr, savepoint)
}

// BytesKeySupported returns true if a db created supports bytes as a key
func (provider *versionedDBProvider) BytesKeySupported() bool {
	return true
}

// Close closes the underlying db
func (provider *versionedDBProvider) Close() {
	provider.dbProvider.Close()
}

// Drop drops channel-specific data from the state leveldb.
// It is not an error if a database does not exist.
func (provider *versionedDBProvider) Drop(dbName string) error {
	return provider.dbProvider.Drop(dbName)
}
