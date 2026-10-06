/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package privacyenabledstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hyperledger/fabric-lib-go/common/metrics/disabled"
	"github.com/hyperledger/fabric/core/ledger"
	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger/bookkeeping"
	testmock "github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/privacyenabledstate/mock"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/statecouchdb"
	"github.com/hyperledger/fabric/core/ledger/mock"
	"github.com/stretchr/testify/require"
)

// TestEnv - an interface that a test environment implements
type TestEnv interface {
	StartExternalResource()
	Init(t testing.TB)
	GetDBHandle(id string) *DB
	GetProvider() *DBProvider
	GetName() string
	Cleanup()
	StopExternalResource()
}

// Tests will be run against each environment in this array
// For example, to skip CouchDB tests, remove &CouchDBLockBasedEnv{}
var testEnvs = []TestEnv{&LevelDBTestEnv{}, &CouchDBTestEnv{}}

// /////////// LevelDB Environment //////////////

// LevelDBTestEnv implements TestEnv interface for leveldb based storage
type LevelDBTestEnv struct {
	t                 testing.TB
	provider          *DBProvider
	bookkeeperTestEnv *bookkeeping.TestEnv
	dbPath            string
}

// Init implements corresponding function from interface TestEnv
func (env *LevelDBTestEnv) Init(t testing.TB) {
	dbPath := t.TempDir()
	env.bookkeeperTestEnv = bookkeeping.NewTestEnv(t, ledger.GoLevelDB)
	dbProvider, err := NewDBProvider(
		env.bookkeeperTestEnv.TestProvider,
		&disabled.Provider{},
		&mock.HealthCheckRegistry{},
		&StateDBConfig{
			StateDBConfig: &ledger.StateDBConfig{
				StateDatabase: ledger.GoLevelDB,
			},
			LevelDBPath: dbPath,
		},
		[]string{"lscc", "_lifecycle"},
	)
	require.NoError(t, err)
	env.t = t
	env.provider = dbProvider
	env.dbPath = dbPath
}

// StartExternalResource will be an empty implementation for levelDB test environment.
func (env *LevelDBTestEnv) StartExternalResource() {
	// empty implementation
}

// StopExternalResource will be an empty implementation for levelDB test environment.
func (env *LevelDBTestEnv) StopExternalResource() {
	// empty implementation
}

// GetDBHandle implements corresponding function from interface TestEnv
func (env *LevelDBTestEnv) GetDBHandle(id string) *DB {
	db, err := env.provider.GetDBHandle(id, nil)
	require.NoError(env.t, err)
	return db
}

// GetProvider returns DBProvider
func (env *LevelDBTestEnv) GetProvider() *DBProvider {
	return env.provider
}

// GetName implements corresponding function from interface TestEnv
func (env *LevelDBTestEnv) GetName() string {
	return "levelDBTestEnv"
}

// Cleanup implements corresponding function from interface TestEnv
func (env *LevelDBTestEnv) Cleanup() {
	env.provider.Close()
	env.bookkeeperTestEnv.Cleanup()
}

// WrapDB wraps the given store in a DB, as a test that brings a store of its own
// making, rather than taking the one of a test environment, is in need of. The
// store it is given is the one of this environment under the given ledger, so
// that the state it keeps is the state of a real db.
func (env *LevelDBTestEnv) WrapDB(ledgerID string, vdb statedb.VersionedDB) *DB {
	db := env.GetDBHandle(ledgerID)
	wrapped, err := NewDB(vdb, ledgerID, db.metadataHint)
	require.NoError(env.t, err)
	return wrapped
}

// /////////// CouchDB Environment //////////////

// CouchDBTestEnv implements TestEnv interface for couchdb based storage
type CouchDBTestEnv struct {
	couchAddress      string
	t                 testing.TB
	provider          *DBProvider
	bookkeeperTestEnv *bookkeeping.TestEnv
	redoPath          string
	couchCleanup      func()
	couchDBConfig     *ledger.CouchDBConfig
}

// StartExternalResource starts external couchDB resources.
func (env *CouchDBTestEnv) StartExternalResource() {
	if env.couchAddress != "" {
		return
	}
	env.couchAddress, env.couchCleanup = statecouchdb.StartCouchDB(env.t.(*testing.T), nil)
}

// StopExternalResource stops external couchDB resources.
func (env *CouchDBTestEnv) StopExternalResource() {
	if env.couchAddress != "" {
		env.couchCleanup()
	}
}

// Init implements corresponding function from interface TestEnv
func (env *CouchDBTestEnv) Init(t testing.TB) {
	redoPath := t.TempDir()

	env.t = t
	env.StartExternalResource()

	stateDBConfig := &StateDBConfig{
		StateDBConfig: &ledger.StateDBConfig{
			StateDatabase: ledger.CouchDB,
			CouchDB: &ledger.CouchDBConfig{
				Address:             env.couchAddress,
				Username:            "admin",
				Password:            "adminpw",
				MaxRetries:          3,
				MaxRetriesOnStartup: 20,
				RequestTimeout:      35 * time.Second,
				InternalQueryLimit:  1000,
				MaxBatchUpdateSize:  1000,
				RedoLogPath:         redoPath,
				RedoLogDBType:       ledger.GoLevelDB,
			},
		},
		LevelDBPath: "",
	}

	env.bookkeeperTestEnv = bookkeeping.NewTestEnv(t, ledger.GoLevelDB)
	dbProvider, err := NewDBProvider(
		env.bookkeeperTestEnv.TestProvider,
		&disabled.Provider{},
		&mock.HealthCheckRegistry{},
		stateDBConfig,
		[]string{"lscc", "_lifecycle"},
	)
	require.NoError(t, err)
	env.provider = dbProvider
	env.redoPath = redoPath
	env.couchDBConfig = stateDBConfig.CouchDB
}

// GetDBHandle implements corresponding function from interface TestEnv
func (env *CouchDBTestEnv) GetDBHandle(id string) *DB {
	db, err := env.provider.GetDBHandle(id, &testmock.ChannelInfoProvider{})
	require.NoError(env.t, err)
	return db
}

// GetProvider returns DBProvider
func (env *CouchDBTestEnv) GetProvider() *DBProvider {
	return env.provider
}

// GetName implements corresponding function from interface TestEnv
func (env *CouchDBTestEnv) GetName() string {
	return "couchDBTestEnv"
}

// Cleanup implements corresponding function from interface TestEnv
func (env *CouchDBTestEnv) Cleanup() {
	if env.provider != nil {
		require.NoError(env.t, statecouchdb.DropApplicationDBs(env.couchDBConfig))
	}
	env.bookkeeperTestEnv.Cleanup()
	env.provider.Close()
}

// /////////// A store that hands out intermediate roots //////////////

// RootsStore is a db that hands out the root of the state after every
// transaction of a block, the way a db that keeps the state in a tree does. The
// state itself is the one of the db it wraps; what it adds is the root.
//
// The root is the hash of the state as it stands, so it moves when, and only
// when, the state does.
type RootsStore struct {
	statedb.VersionedDB
	dbName string
	state  map[string]string
}

// NewRootsStore wraps the given db and starts the state it keeps for the roots
// empty.
func NewRootsStore(vdb statedb.VersionedDB, dbName string) *RootsStore {
	return &RootsStore{VersionedDB: vdb, dbName: dbName, state: map[string]string{}}
}

func (s *RootsStore) ApplyUpdates(batch *statedb.UpdateBatch, height *version.Height) error {
	return s.applyBatch(batch)
}

// ApplyUpdatesWithRoots implements statedb.IntermediateRoots.
func (s *RootsStore) ApplyUpdatesWithRoots(
	batch *statedb.UpdateBatch,
	height *version.Height,
	perTx []*statedb.UpdateBatch,
	reporter statedb.RootReporter,
) error {
	// A block with no boundaries is one transaction as far as this store is
	// concerned, which is the whole of the block as far as it is concerned.
	if len(perTx) == 0 {
		if err := s.applyBatch(batch); err != nil {
			return err
		}
		if err := s.VersionedDB.ApplyUpdates(batch, height); err != nil {
			return err
		}
		if height != nil {
			reporter.BlockRoot(s.dbName, height.BlockNum, s.root())
		}
		return nil
	}

	merged := statedb.NewUpdateBatch()
	root := s.root()
	for txNum, txBatch := range perTx {
		if err := s.applyBatch(txBatch); err != nil {
			return err
		}
		if txRoot := s.root(); !bytes.Equal(txRoot, root) {
			root = txRoot
			if height != nil {
				reporter.TxRoot(s.dbName, height.BlockNum, uint64(txNum), root)
			}
		}
		merged.Merge(txBatch)
	}
	if err := s.VersionedDB.ApplyUpdates(merged, height); err != nil {
		return err
	}
	if height != nil {
		reporter.BlockRoot(s.dbName, height.BlockNum, root)
	}
	return nil
}

func (s *RootsStore) applyBatch(batch *statedb.UpdateBatch) error {
	if batch == nil {
		return nil
	}
	for _, ns := range batch.GetUpdatedNamespaces() {
		for key, vv := range batch.GetUpdates(ns) {
			if vv.Value == nil {
				delete(s.state, ns+"\x00"+key)
				continue
			}
			s.state[ns+"\x00"+key] = string(vv.Value)
		}
	}
	return nil
}

func (s *RootsStore) root() []byte {
	lines := make([]string, 0, len(s.state))
	for key, value := range s.state {
		lines = append(lines, key+"\x00"+value)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return sum[:]
}

// ReportedRoot is one root as it was reported, with the channel and the block
// and the transaction it was named after.
type ReportedRoot struct {
	Channel string
	Height  uint64
	TxNum   uint64
	Root    string
}

// RootRecorder remembers the roots it is handed, in the order it is handed them.
type RootRecorder struct {
	TxRoots    []ReportedRoot
	BlockRoots []ReportedRoot
}

// BlockRoot implements statedb.RootReporter.
func (r *RootRecorder) BlockRoot(channel string, height uint64, root []byte) {
	r.BlockRoots = append(r.BlockRoots, ReportedRoot{channel, height, 0, hex.EncodeToString(root)})
}

// TxRoot implements statedb.RootReporter.
func (r *RootRecorder) TxRoot(channel string, height uint64, txNum uint64, root []byte) {
	r.TxRoots = append(r.TxRoots, ReportedRoot{channel, height, txNum, hex.EncodeToString(root)})
}

// GC implements statedb.RootReporter.
func (r *RootRecorder) GC(channel string, liveNodes int, reclaimedNodes int, durationMillis float64) {
}
