/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"fmt"
	"os"

	"gopkg.in/alecthomas/kingpin.v2"
)

const (
	helpText = `Migrate LevelDB databases to PebbleDB.

This tool migrates one or more LevelDB directories to PebbleDB. It must be
run on a stopped peer or orderer node. After migration, only the
configuration needs to be updated.

Typical directories to migrate:

  Peer:
    ledgersData/stateLeveldb
    ledgersData/historyLeveldb
    ledgersData/bookkeeper
    ledgersData/pvtdataStore
    ledgersData/configHistory
    ledgersData/chains/index
    ledgersData/ledgerProvider
    ledgersData/couchdbRedologs
    transientstore

  Orderer:
    <ordererDir>/index

After migration, remove old LevelDB file lock directories manually
because PebbleDB uses a regular file (syscall.Flock) instead:

  Peer:
    <ledgersData>/fileLock
    <peerFS>/transientStoreFileLock

  Orderer: (none — orderer does not use FileLock)
`
)

var (
	app     = kingpin.New("dbmigrator", "Database migration tool: LevelDB to PebbleDB")
	dbPaths = app.Flag("db-path", "Path to LevelDB directory to migrate (repeatable)").Required().Strings()
)

func init() {
	app.Help = helpText
}

func main() {
	kingpin.Version("0.0.1")

	_, err := app.Parse(os.Args[1:])
	if err != nil {
		app.Usage(os.Args[1:])
		fmt.Fprintf(os.Stderr, "\nError: %s\n", err)
		os.Exit(1)
	}

	for _, dbPath := range *dbPaths {
		fmt.Printf("Begin of migration %s\n", dbPath)
		if err = migrateDB(dbPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error migrating %s: %s\n", dbPath, err)
			os.Exit(1)
		}
		fmt.Printf("Successfully migrated %s\n", dbPath)
	}

	fmt.Print(`Migration completed. To activate PebbleDB:
   1. Backups of old directories are in packages with the bak extension
   2. Remove old LevelDB file lock directories (see --help for paths)
   3. Update the configuration:
   - core.yaml: ledger.statedbase: pebbledb and ledger.state.statedbase: pebbledb
   - orderer.yaml: FileLedger.statedbase: pebbledb
   4. Start the node`)
}
