/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperledger/fabric/core/ledger"
	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/statekvdb"
	"github.com/stretchr/testify/require"
)

// TestMigratedChannelContinuesFromTheSameBlock runs a channel on the flat store,
// moves its state onto a trie with the dbmigrator program, and opens the channel
// again on the database the migration wrote. The channel is at the same block
// with the same state as it was left at, and it carries its history on from
// there: the next block is committed on top of the state that was migrated.
//
// The channel is migrated by the program itself, not by a copy of what it does,
// so that what is exercised here is the migration an operator runs.
func TestMigratedChannelContinuesFromTheSameBlock(t *testing.T) {
	env := newEnvWithStateDB(t, ledger.GoLevelDB)
	defer env.cleanup()
	env.initLedgerMgmt()

	dataHelper := newSampleDataHelper(t)
	l := env.createTestLedgerFromGenesisBlk("ledger1")
	dataHelper.populateLedger(l)
	dataHelper.verifyLedgerContent(l)
	bcInfo, err := l.lgr.GetBlockchainInfo()
	require.NoError(t, err)
	env.closeLedgerMgmt()

	rootFS := env.initializer.Config.RootFSPath
	statePath := kvledger.StateDBPath(rootFS)
	migratedPath := statePath + ".trie"

	// The root the migrated state is expected to have is computed from the state
	// the flat store held, read out of its own LevelDB, so that the check on the
	// migrated trie does not take the word of the trie for its own root.
	sourcePairs := flatStatePairs(t, statePath, "ledger1")
	sourceRoot := stateRootOfPairs(t, sourcePairs)

	runDBMigrator(t, "migrate",
		"--source", statePath,
		"--target", migratedPath,
		"--to", "leveldbtrie",
		"--file-lock", filepath.Join(rootFS, "fileLock"),
	)

	// What the migration wrote is what the peer is handed, which is the whole of
	// what an operator does with it beside stopping the peer.
	require.NoError(t, os.RemoveAll(statePath))
	require.NoError(t, os.Rename(migratedPath, statePath))

	env.initializer.Config.StateDBConfig.StateDatabase = ledger.LevelDBTrie
	env.initLedgerMgmt()
	l = env.openTestLedger("ledger1")

	l.verifyLedgerHeight(bcInfo.GetHeight())
	dataHelper.verifyLedgerContent(l)

	env.closeLedgerMgmt()
	require.Equal(t, sourceRoot, stateRootInStore(t, rootFS, "ledger1"),
		"the root of the migrated state is not the root computed over the state it was migrated from")

	env.initLedgerMgmt()
	l = env.openTestLedger("ledger1")

	// The state that was migrated is carried on by the store that took it over.
	l.simulateDataTx("tx-after-migration", func(s *simulator) {
		s.setState("cc1", "key-after-migration", "value-after-migration")
	})
	l.cutBlockAndCommitLegacy()
	l.verifyPubState("cc1", "key-after-migration", "value-after-migration")
	dataHelper.verifyLedgerContent(l)

	actualBcInfo, err := l.lgr.GetBlockchainInfo()
	require.NoError(t, err)
	require.Equal(t, bcInfo.GetHeight()+1, actualBcInfo.GetHeight())

	// The block committed on top of the migrated state moves the root to the
	// root of the state the flat store held together with the write of that
	// block, which is what an independent computation of the root expects.
	encoded, err := statekvdb.EncodeValue(&statedb.VersionedValue{
		Value:   []byte("value-after-migration"),
		Version: version.NewHeight(actualBcInfo.GetHeight()-1, 0),
	})
	require.NoError(t, err)
	sourcePairs[string(append(append([]byte("cc1"), 0x00), "key-after-migration"...))] = encoded

	env.closeLedgerMgmt()
	require.Equal(t, stateRootOfPairs(t, sourcePairs), stateRootInStore(t, rootFS, "ledger1"),
		"the root after the first block on the migrated state is not the root computed over the state that block applied")
	env.initLedgerMgmt()
}

// TestMigratedBackChannelContinuesFromTheSameBlock runs a channel on the trie,
// moves its state back onto the flat store with the dbmigrator program, and
// opens the channel again on the database the migration wrote. The channel is at
// the same block with the same state as it was left at, and it carries its
// history on from there, so that an operator who leaves the trie behind is not
// left with a channel that has to be rebuilt.
func TestMigratedBackChannelContinuesFromTheSameBlock(t *testing.T) {
	env := newEnvWithStateDB(t, ledger.LevelDBTrie)
	defer env.cleanup()
	env.initLedgerMgmt()

	dataHelper := newSampleDataHelper(t)
	l := env.createTestLedgerFromGenesisBlk("ledger1")
	dataHelper.populateLedger(l)
	dataHelper.verifyLedgerContent(l)
	bcInfo, err := l.lgr.GetBlockchainInfo()
	require.NoError(t, err)
	env.closeLedgerMgmt()

	rootFS := env.initializer.Config.RootFSPath
	statePath := kvledger.StateDBPath(rootFS)
	migratedPath := statePath + ".leveldb"

	runDBMigrator(t, "migrate",
		"--source", statePath,
		"--target", migratedPath,
		"--to", "goleveldb",
		"--file-lock", filepath.Join(rootFS, "fileLock"),
	)

	require.NoError(t, os.RemoveAll(statePath))
	require.NoError(t, os.Rename(migratedPath, statePath))

	env.initializer.Config.StateDBConfig.StateDatabase = ledger.GoLevelDB
	env.initLedgerMgmt()
	l = env.openTestLedger("ledger1")

	l.verifyLedgerHeight(bcInfo.GetHeight())
	dataHelper.verifyLedgerContent(l)

	// The state that was migrated is carried on by the store that took it over.
	l.simulateDataTx("tx-after-migration", func(s *simulator) {
		s.setState("cc1", "key-after-migration", "value-after-migration")
	})
	l.cutBlockAndCommitLegacy()
	l.verifyPubState("cc1", "key-after-migration", "value-after-migration")
	dataHelper.verifyLedgerContent(l)

	actualBcInfo, err := l.lgr.GetBlockchainInfo()
	require.NoError(t, err)
	require.Equal(t, bcInfo.GetHeight()+1, actualBcInfo.GetHeight())
}

// runDBMigrator builds the dbmigrator program out of this checkout and runs it
// with the given arguments. The program is built rather than called, because it
// is a program of the repository and the boundary being tested is the one an
// operator crosses by running it.
func runDBMigrator(t *testing.T, args ...string) {
	t.Helper()

	root := moduleRoot(t)
	binary := filepath.Join(t.TempDir(), "dbmigrator")

	build := exec.Command("go", "build", "-o", binary, "./cmd/dbmigrator")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cannot build the dbmigrator program: %v\n%s", err, output)
	}

	run := exec.Command(binary, args...)
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("dbmigrator %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}

// moduleRoot returns the directory of the module this checkout is, which is
// where the programs of the repository are built from.
func moduleRoot(t *testing.T) string {
	t.Helper()

	output, err := exec.Command("go", "env", "GOMOD").Output()
	require.NoError(t, err)
	modFile := strings.TrimSpace(string(output))
	require.NotEmpty(t, modFile, "this checkout is not in a Go module")
	return filepath.Dir(modFile)
}
