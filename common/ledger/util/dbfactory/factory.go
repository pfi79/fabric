/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package dbfactory

import (
	db "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/common/ledger/util/leveldbhelper"
	"github.com/hyperledger/fabric/common/ledger/util/pebblehelper"
)

// CreateDB creates a database of the specified type.
func CreateDB(dbType string, dbPath string, expectedFormat string) db.DB {
	switch dbType {
	case db.PebbleDB:
		return pebblehelper.CreateDB(&pebblehelper.Conf{
			DBPath:         dbPath,
			ExpectedFormat: expectedFormat,
		})
	default:
		return leveldbhelper.CreateDB(&leveldbhelper.Conf{
			DBPath:         dbPath,
			ExpectedFormat: expectedFormat,
		})
	}
}

// NewProvider creates a provider of the specified type.
func NewProvider(dbType string, dbPath string, expectedFormat string) (db.Provider, error) {
	switch dbType {
	case db.PebbleDB:
		return pebblehelper.NewProvider(&pebblehelper.Conf{
			DBPath:         dbPath,
			ExpectedFormat: expectedFormat,
		})
	default:
		return leveldbhelper.NewProvider(&leveldbhelper.Conf{
			DBPath:         dbPath,
			ExpectedFormat: expectedFormat,
		})
	}
}

// NewFileLock creates a FileLock based on the dbType.
func NewFileLock(dbType, filePath string) db.FileLock {
	switch dbType {
	case db.PebbleDB:
		return pebblehelper.NewFileLock(filePath)
	default:
		return leveldbhelper.NewFileLock(filePath)
	}
}

func RetrieveDataFormatInfo(dbType, dbPath string) (formatVerison string, isDBEmpty bool, err error) {
	switch dbType {
	case db.PebbleDB:
		return pebblehelper.RetrieveDataFormatInfo(dbPath)
	default:
		return leveldbhelper.RetrieveDataFormatInfo(dbPath)
	}
}
