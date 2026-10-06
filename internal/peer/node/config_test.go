/*
Copyright IBM Corp. 2016 All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package node

import (
	"testing"
	"time"

	"github.com/hyperledger/fabric/core/ledger"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/leveldbtrie"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// The choice of the state database is resolved while the configuration is
// parsed, so an unset choice does not travel to the ledger as an empty type.
func TestLedgerConfigUnsetStateDatabase(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("peer.fileSystemPath", "/peerfs")

	conf := ledgerConfig()

	require.Equal(t, ledger.GoLevelDB, conf.StateDatabase)
	require.Equal(t, ledger.GoLevelDB, conf.StateDBConfig.StateDatabase)
}

func TestLedgerConfig(t *testing.T) {
	defer viper.Reset()
	tests := []struct {
		name     string
		config   map[string]any
		expected *ledger.Config
	}{
		{
			name: ledger.GoLevelDB,
			config: map[string]any{
				"peer.fileSystemPath":        "/peerfs",
				"ledger.state.stateDatabase": ledger.GoLevelDB,
			},
			expected: &ledger.Config{
				RootFSPath:    "/peerfs/ledgersData",
				StateDatabase: ledger.GoLevelDB,
				StateDBConfig: &ledger.StateDBConfig{
					StateDatabase: ledger.GoLevelDB,
					CouchDB:       &ledger.CouchDBConfig{},
				},
				PrivateDataConfig: &ledger.PrivateDataConfig{
					MaxBatchSize:                        5000,
					BatchesInterval:                     1000,
					PurgeInterval:                       100,
					DeprioritizedDataReconcilerInterval: 60 * time.Minute,
					PurgedKeyAuditLogging:               true,
				},
				HistoryDBConfig: &ledger.HistoryDBConfig{
					Enabled: false,
				},
				SnapshotsConfig: &ledger.SnapshotsConfig{
					RootDir: "/peerfs/snapshots",
				},
			},
		},
		{
			// The store type is no longer configurable, so a configuration
			// written by an operator that still names a store must not change
			// the type of the internal KV stores.
			name: "store type is not configurable",
			config: map[string]any{
				"peer.fileSystemPath":        "/peerfs",
				"ledger.stateDatabase":       "someKVStore",
				"ledger.state.stateDatabase": ledger.GoLevelDB,
			},
			expected: &ledger.Config{
				RootFSPath:    "/peerfs/ledgersData",
				StateDatabase: ledger.GoLevelDB,
				StateDBConfig: &ledger.StateDBConfig{
					StateDatabase: ledger.GoLevelDB,
					CouchDB:       &ledger.CouchDBConfig{},
				},
				PrivateDataConfig: &ledger.PrivateDataConfig{
					MaxBatchSize:                        5000,
					BatchesInterval:                     1000,
					PurgeInterval:                       100,
					DeprioritizedDataReconcilerInterval: 60 * time.Minute,
					PurgedKeyAuditLogging:               true,
				},
				HistoryDBConfig: &ledger.HistoryDBConfig{
					Enabled: false,
				},
				SnapshotsConfig: &ledger.SnapshotsConfig{
					RootDir: "/peerfs/snapshots",
				},
			},
		},
		{
			name: "CouchDB Defaults",
			config: map[string]any{
				"peer.fileSystemPath":                              "/peerfs",
				"ledger.state.stateDatabase":                       "CouchDB",
				"ledger.state.couchDBConfig.couchDBAddress":        "localhost:5984",
				"ledger.state.couchDBConfig.username":              "username",
				"ledger.state.couchDBConfig.password":              "password",
				"ledger.state.couchDBConfig.maxRetries":            3,
				"ledger.state.couchDBConfig.maxRetriesOnStartup":   10,
				"ledger.state.couchDBConfig.requestTimeout":        "30s",
				"ledger.state.couchDBConfig.createGlobalChangesDB": true,
				"ledger.state.couchDBConfig.cacheSize":             64,
			},
			expected: &ledger.Config{
				RootFSPath:    "/peerfs/ledgersData",
				StateDatabase: ledger.GoLevelDB,
				StateDBConfig: &ledger.StateDBConfig{
					StateDatabase: "CouchDB",
					CouchDB: &ledger.CouchDBConfig{
						Address:               "localhost:5984",
						Username:              "username",
						Password:              "password",
						MaxRetries:            3,
						MaxRetriesOnStartup:   10,
						RequestTimeout:        30 * time.Second,
						InternalQueryLimit:    1000,
						MaxBatchUpdateSize:    500,
						CreateGlobalChangesDB: true,
						RedoLogPath:           "/peerfs/ledgersData/couchdbRedoLogs",
						RedoLogDBType:         ledger.GoLevelDB,
						UserCacheSizeMBs:      64,
					},
				},
				PrivateDataConfig: &ledger.PrivateDataConfig{
					MaxBatchSize:                        5000,
					BatchesInterval:                     1000,
					PurgeInterval:                       100,
					DeprioritizedDataReconcilerInterval: 60 * time.Minute,
					PurgedKeyAuditLogging:               true,
				},
				HistoryDBConfig: &ledger.HistoryDBConfig{
					Enabled: false,
				},
				SnapshotsConfig: &ledger.SnapshotsConfig{
					RootDir: "/peerfs/snapshots",
				},
			},
		},
		{
			name: "CouchDB Explicit",
			config: map[string]any{
				"peer.fileSystemPath":                                     "/peerfs",
				"ledger.state.stateDatabase":                              "CouchDB",
				"ledger.state.couchDBConfig.couchDBAddress":               "localhost:5984",
				"ledger.state.couchDBConfig.username":                     "username",
				"ledger.state.couchDBConfig.password":                     "password",
				"ledger.state.couchDBConfig.maxRetries":                   3,
				"ledger.state.couchDBConfig.maxRetriesOnStartup":          10,
				"ledger.state.couchDBConfig.requestTimeout":               "30s",
				"ledger.state.couchDBConfig.internalQueryLimit":           500,
				"ledger.state.couchDBConfig.maxBatchUpdateSize":           600,
				"ledger.state.couchDBConfig.createGlobalChangesDB":        true,
				"ledger.state.couchDBConfig.cacheSize":                    64,
				"ledger.pvtdataStore.collElgProcMaxDbBatchSize":           50000,
				"ledger.pvtdataStore.collElgProcDbBatchesInterval":        10000,
				"ledger.pvtdataStore.purgeInterval":                       1000,
				"ledger.pvtdataStore.purgedKeyAuditLogging":               false,
				"ledger.pvtdataStore.deprioritizedDataReconcilerInterval": "180m",
				"ledger.history.enableHistoryDatabase":                    true,
				"ledger.snapshots.rootDir":                                "/peerfs/customLocationForsnapshots",
			},
			expected: &ledger.Config{
				RootFSPath:    "/peerfs/ledgersData",
				StateDatabase: ledger.GoLevelDB,
				StateDBConfig: &ledger.StateDBConfig{
					StateDatabase: "CouchDB",
					CouchDB: &ledger.CouchDBConfig{
						Address:               "localhost:5984",
						Username:              "username",
						Password:              "password",
						MaxRetries:            3,
						MaxRetriesOnStartup:   10,
						RequestTimeout:        30 * time.Second,
						InternalQueryLimit:    500,
						MaxBatchUpdateSize:    600,
						CreateGlobalChangesDB: true,
						RedoLogPath:           "/peerfs/ledgersData/couchdbRedoLogs",
						RedoLogDBType:         ledger.GoLevelDB,
						UserCacheSizeMBs:      64,
					},
				},
				PrivateDataConfig: &ledger.PrivateDataConfig{
					MaxBatchSize:                        50000,
					BatchesInterval:                     10000,
					PurgeInterval:                       1000,
					DeprioritizedDataReconcilerInterval: 180 * time.Minute,
					PurgedKeyAuditLogging:               false,
				},
				HistoryDBConfig: &ledger.HistoryDBConfig{
					Enabled: true,
				},
				SnapshotsConfig: &ledger.SnapshotsConfig{
					RootDir: "/peerfs/customLocationForsnapshots",
				},
			},
		},
	}

	for _, test := range tests {
		_test := test
		t.Run(_test.name, func(t *testing.T) {
			for k, v := range _test.config {
				viper.Set(k, v)
			}
			conf := ledgerConfig()
			require.Equal(t, _test.expected, conf)
		})
	}
}

// The configuration of the trie is read as a whole: a key that is left out is
// given the default of the specification, and a key that is set is taken at its
// word, so that a walk turned off can be told from a walk never mentioned.
func TestLedgerConfigLevelDBTrie(t *testing.T) {
	defer viper.Reset()
	tests := []struct {
		name     string
		config   map[string]any
		expected *leveldbtrie.Conf
	}{
		{
			name: "defaults",
			config: map[string]any{
				"ledger.state.stateDatabase": ledger.LevelDBTrie,
			},
			expected: &leveldbtrie.Conf{
				VerifyOnOpen:     true,
				KeepRoots:        2,
				GCIntervalBlocks: 1000,
				ExactMetrics:     false,
			},
		},
		{
			name: "all keys set",
			config: map[string]any{
				"ledger.state.stateDatabase":                ledger.LevelDBTrie,
				"ledger.state.leveldbtrie.verifyOnOpen":     false,
				"ledger.state.leveldbtrie.keepRoots":        5,
				"ledger.state.leveldbtrie.gcIntervalBlocks": 250,
				"ledger.state.leveldbtrie.exactMetrics":     true,
			},
			expected: &leveldbtrie.Conf{
				VerifyOnOpen:     false,
				KeepRoots:        5,
				GCIntervalBlocks: 250,
				ExactMetrics:     true,
			},
		},
		{
			// A walk set off must stay off: the whole configuration is built,
			// so the key is not confused with one that was never mentioned.
			name: "walk off",
			config: map[string]any{
				"ledger.state.stateDatabase":            ledger.LevelDBTrie,
				"ledger.state.leveldbtrie.verifyOnOpen": false,
			},
			expected: &leveldbtrie.Conf{
				VerifyOnOpen:     false,
				KeepRoots:        2,
				GCIntervalBlocks: 1000,
				ExactMetrics:     false,
			},
		},
	}

	for _, test := range tests {
		_test := test
		t.Run(_test.name, func(t *testing.T) {
			viper.Reset()
			viper.Set("peer.fileSystemPath", "/peerfs")
			for k, v := range _test.config {
				viper.Set(k, v)
			}
			conf := ledgerConfig()
			require.Equal(t, ledger.LevelDBTrie, conf.StateDBConfig.StateDatabase)
			require.Equal(t, _test.expected, conf.StateDBConfig.LevelDBTrie)
		})
	}
}

// A store that is not the trie is left to the choice that was there before it:
// the configuration of the trie is not built at all, and the name of an unset
// or unknown store is resolved the way it always was.
func TestLedgerConfigWithoutLevelDBTrie(t *testing.T) {
	defer viper.Reset()
	tests := []struct {
		name     string
		config   map[string]any
		expected string
	}{
		{
			name:     "unset",
			config:   nil,
			expected: ledger.GoLevelDB,
		},
		{
			name:     "empty",
			config:   map[string]any{"ledger.state.stateDatabase": ""},
			expected: ledger.GoLevelDB,
		},
		{
			name:     "unknown",
			config:   map[string]any{"ledger.state.stateDatabase": "someUnknownStore"},
			expected: "someUnknownStore",
		},
		{
			name:     "top level name is ignored",
			config:   map[string]any{"ledger.stateDatabase": "someUnknownStore"},
			expected: ledger.GoLevelDB,
		},
	}

	for _, test := range tests {
		_test := test
		t.Run(_test.name, func(t *testing.T) {
			viper.Reset()
			viper.Set("peer.fileSystemPath", "/peerfs")
			for k, v := range _test.config {
				viper.Set(k, v)
			}
			conf := ledgerConfig()
			require.Equal(t, _test.expected, conf.StateDBConfig.StateDatabase)
			require.Nil(t, conf.StateDBConfig.LevelDBTrie)
		})
	}
}
