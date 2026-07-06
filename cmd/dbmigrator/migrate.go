/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"fmt"
	"os"

	db "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/common/ledger/util/leveldbhelper"
	"github.com/hyperledger/fabric/common/ledger/util/pebblehelper"
	"github.com/pkg/errors"
)

const batchSizeBytes = 10 * 1024 * 1024 // 10MB

func migrateDB(sourcePath string) error {
	if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
		return fmt.Errorf("source directory does not exist: %s", sourcePath)
	}

	targetPath := sourcePath + "_pebble"

	if _, err := os.Stat(targetPath); err == nil {
		return fmt.Errorf("target directory already exists: %s; remove it or choose a different source", targetPath)
	}

	sourceDB := leveldbhelper.CreateDB(&leveldbhelper.Conf{DBPath: sourcePath})
	sourceDB.Open()

	targetDB := pebblehelper.CreateDB(&pebblehelper.Conf{DBPath: targetPath})
	targetDB.Open()

	if err := copyDB(sourceDB, targetDB); err != nil {
		sourceDB.Close()
		targetDB.Close()
		os.RemoveAll(targetPath)
		return err
	}

	sourceDB.Close()
	targetDB.Close()

	backupPath := sourcePath + ".bak"
	if err := os.Rename(sourcePath, backupPath); err != nil {
		return err
	}
	if err := os.Rename(targetPath, sourcePath); err != nil {
		os.Rename(backupPath, sourcePath)
		return err
	}

	return nil
}

func copyDB(source, target db.DB) error {
	itr, err := source.GetIterator(nil, nil)
	if err != nil {
		return errors.Wrap(err, "error creating iterator")
	}
	defer itr.Release()

	batch := target.NewUpdateBatch()
	batchSize := 0
	totalKeys := 0

	for itr.Next() {
		key := make([]byte, len(itr.Key()))
		copy(key, itr.Key())
		value := make([]byte, len(itr.Value()))
		copy(value, itr.Value())

		batch.Put(key, value)
		batchSize += len(key) + len(value)
		totalKeys++

		if batchSize >= batchSizeBytes {
			if err = target.WriteBatch(batch, true); err != nil {
				return errors.Wrap(err, "error writing batch")
			}
			fmt.Printf("\rMigrated %d keys", totalKeys)
			batch = target.NewUpdateBatch()
			batchSize = 0
		}
	}

	if err = itr.Error(); err != nil {
		return errors.Wrap(err, "iterator error")
	}

	if batchSize > 0 {
		if err = target.WriteBatch(batch, true); err != nil {
			return errors.Wrap(err, "error writing final batch")
		}
	}

	fmt.Printf("\rMigrated %d keys\n", totalKeys)
	return nil
}
