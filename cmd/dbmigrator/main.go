/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// dbmigrator moves the world state of a peer from one of the two shapes it can
// be kept in into the other: the flat store of LevelDB, and the store that keeps
// the state of a channel in a patricia merkle trie over the same LevelDB.
//
// The database that is handed over to it is opened for reading and for nothing
// else, and is not written to at any point of a run. What is written goes into a
// directory beside the database that is being written and is named that database
// only after the whole of it has been checked against what was migrated.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/hyperledger/fabric/common/metadata"
	"gopkg.in/alecthomas/kingpin.v2"
)

const (
	sourceDesc = "Directory of the LevelDB the state of a peer is in. It is opened for reading only and is never written to. " +
		"Both formats of the state live under the same path of a peer, so this is the 'stateLeveldb' directory of the peer."
	targetDesc = "Directory the state is written into, beside which the migration works. It is created where it does not exist, " +
		"and what is written is moved into it only after it has been checked, so a migration that is stopped leaves it as it was."
	toDesc      = "Format the state is written in: 'leveldbtrie' for the store that keeps the state of a channel in a patricia merkle trie, 'goleveldb' for the flat store."
	channelDesc = "Channel to migrate. May be given more than once. Every channel of the database is migrated where no channel is named."
	batchDesc   = "How much of the state is written as one version of the store it is written into, in bytes. This is what the memory a migration holds at once is bounded by, and nothing else is: a state of any size is migrated a batch at a time."
	verifyDesc  = "Check the result against the state that was migrated: the number of keys and every pair of key and value, along with the metadata and the version of the value. --no-verify goes ahead without the check."
	lockDesc    = "Path of the file lock of the peer whose state is being migrated, which is the 'fileLock' file beside the source directory. A peer that is running holds it and a migration refuses to begin."
	confirmDesc = "Remove the source database. Without this the run says what it would remove and removes nothing."
)

var (
	app = kingpin.New("dbmigrator", "Migrates the world state of a peer between the flat LevelDB store and the patricia merkle trie store, leaving the database it was handed over whole.")

	migrateCmd  = app.Command("migrate", "Copies the world state of a peer into a database of another format.")
	migrateFrom = migrateCmd.Flag("source", sourceDesc).Required().String()
	migrateTo   = migrateCmd.Flag("target", targetDesc).Required().String()
	migrateWhat = migrateCmd.Flag("to", toDesc).Required().String()
	migrateWho  = migrateCmd.Flag("channel", channelDesc).Strings()
	migrateSize = migrateCmd.Flag("batch-size", batchDesc).Default(strconv.Itoa(defaultBatchSize)).Int()
	migrateSee  = migrateCmd.Flag("verify", verifyDesc).Default("true").Bool()
	migrateLock = migrateCmd.Flag("file-lock", lockDesc).String()

	purgeCmd  = app.Command("purge", "Removes the database a state was migrated out of, once the database it was migrated into has been found to hold the whole of it.")
	purgeFrom = purgeCmd.Flag("source", sourceDesc).Required().String()
	purgeTo   = purgeCmd.Flag("target", targetDesc).Required().String()
	purgeLock = purgeCmd.Flag("file-lock", lockDesc).String()
	purgeYes  = purgeCmd.Flag("confirm", confirmDesc).Bool()

	args = os.Args[1:]
)

func main() {
	app.Version(metadata.Version)

	command, err := app.Parse(args)
	if err != nil {
		kingpin.Fatalf("parsing arguments: %s. Try --help", err)
		return
	}

	switch command {
	case migrateCmd.FullCommand():
		err = runMigrate()
	case purgeCmd.FullCommand():
		err = runPurge()
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "dbmigrator: %v\n", err)
		os.Exit(1)
	}
}

// runMigrate reads what was asked for off the command line and hands it to the
// migration. What the command line names is checked here, where a mistake can be
// named as a mistake, rather than inside the run, where it would look like a
// database that cannot be read.
func runMigrate() error {
	format, err := parseFormat(*migrateWhat)
	if err != nil {
		return err
	}
	if err := checkDirectory(*migrateFrom, "source"); err != nil {
		return err
	}

	return migrate(migrateOptions{
		sourcePath: *migrateFrom,
		targetPath: *migrateTo,
		to:         format,
		channels:   *migrateWho,
		batchSize:  *migrateSize,
		verify:     *migrateSee,
		fileLock:   pathOfFileLock(*migrateLock, *migrateFrom),
	})
}

// runPurge reads what was asked for off the command line and hands it to the
// purge.
func runPurge() error {
	if err := checkDirectory(*purgeFrom, "source"); err != nil {
		return err
	}
	if err := checkDirectory(*purgeTo, "target"); err != nil {
		return err
	}
	if same, err := sameDirectory(*purgeFrom, *purgeTo); err != nil {
		return err
	} else if same {
		return fmt.Errorf("the source [%s] and the target [%s] are one and the same database: purging it would remove the state that was to be kept", *purgeFrom, *purgeTo)
	}

	return purge(purgeOptions{
		sourcePath: *purgeFrom,
		targetPath: *purgeTo,
		fileLock:   pathOfFileLock(*purgeLock, *purgeFrom),
		confirm:    *purgeYes,
	})
}

// checkDirectory refuses a path that names nothing, or that names something that
// is not a directory. A path of an operator's is checked before it is opened, so
// that what is wrong with it is said rather than found out from a database.
func checkDirectory(path, what string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return fmt.Errorf("the %s [%s] does not exist", what, path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("the %s [%s] is not a directory", what, path)
	}
	return nil
}

func sameDirectory(left, right string) (bool, error) {
	leftPath, err := filepath.Abs(left)
	if err != nil {
		return false, err
	}
	rightPath, err := filepath.Abs(right)
	if err != nil {
		return false, err
	}
	return leftPath == rightPath, nil
}

// pathOfFileLock returns where the lock of the peer whose state is being
// migrated is kept: where it was named, or, where it was not, where a peer keeps
// it, which is the file of that name beside the directory of its state.
func pathOfFileLock(named, source string) string {
	if named != "" {
		return named
	}
	return filepath.Join(filepath.Dir(filepath.Clean(source)), "fileLock")
}
