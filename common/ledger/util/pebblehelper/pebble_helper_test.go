/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package pebblehelper

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/hyperledger/fabric/common/ledger/util/db"
	"github.com/stretchr/testify/require"
)

var testDBPath = "/tmp/fabric/ledgertests/util/pebblehelper"

type testDBEnv struct {
	t    *testing.T
	path string
	db   db.DB
}

type testDBProviderEnv struct {
	t        *testing.T
	path     string
	provider db.Provider
}

func newTestDBEnv(t *testing.T, path string) *testDBEnv {
	testDBEnv := &testDBEnv{t: t, path: path}
	testDBEnv.cleanup()
	testDBEnv.db = CreateDB(&Conf{DBPath: path})
	return testDBEnv
}

func newTestProviderEnv(t *testing.T, path string) *testDBProviderEnv {
	testProviderEnv := &testDBProviderEnv{t: t, path: path}
	testProviderEnv.cleanup()
	var err error
	testProviderEnv.provider, err = NewProvider(&Conf{DBPath: path})
	if err != nil {
		panic(err)
	}
	return testProviderEnv
}

func (dbEnv *testDBEnv) cleanup() {
	if dbEnv.db != nil {
		dbEnv.db.Close()
	}
	require.NoError(dbEnv.t, os.RemoveAll(dbEnv.path))
}

func (providerEnv *testDBProviderEnv) cleanup() {
	if providerEnv.provider != nil {
		providerEnv.provider.Close()
	}
	require.NoError(providerEnv.t, os.RemoveAll(providerEnv.path))
}

func TestPebbleDBHelperWriteWithoutOpen(t *testing.T) {
	env := newTestDBEnv(t, testDBPath)
	defer env.cleanup()
	db := env.db
	defer func() {
		if recover() == nil {
			t.Fatalf("A panic is expected when writing to db before opening")
		}
	}()
	require.NoError(t, db.Put([]byte("key"), []byte("value"), false))
}

func TestPebbleDBHelperReadWithoutOpen(t *testing.T) {
	env := newTestDBEnv(t, testDBPath)
	defer env.cleanup()
	db := env.db
	defer func() {
		if recover() == nil {
			t.Fatalf("A panic is expected when writing to db before opening")
		}
	}()
	_, err := db.Get([]byte("key"))
	require.NoError(t, err)
}

func TestPebbleDBHelper(t *testing.T) {
	env := newTestDBEnv(t, testDBPath)
	defer env.cleanup()
	db := env.db

	db.Open()
	db.Open()
	IsEmpty, err := db.IsEmpty()
	require.NoError(t, err)
	require.True(t, IsEmpty)
	require.NoError(t, db.Put([]byte("key1"), []byte("value1"), false))
	require.NoError(t, db.Put([]byte("key2"), []byte("value2"), true))
	require.NoError(t, db.Put([]byte("key3"), []byte("value3"), true))

	val, _ := db.Get([]byte("key2"))
	require.Equal(t, "value2", string(val))

	require.NoError(t, db.Delete([]byte("key1"), false))
	require.NoError(t, db.Delete([]byte("key2"), true))

	val1, err1 := db.Get([]byte("key1"))
	require.NoError(t, err1, "")
	require.Equal(t, "", string(val1))

	val2, err2 := db.Get([]byte("key2"))
	require.NoError(t, err2, "")
	require.Equal(t, "", string(val2))

	db.Close()
	db.Close()

	_, err = db.IsEmpty()
	require.Error(t, err)

	val3, err3 := db.Get([]byte("key3"))
	require.Error(t, err3)
	require.Equal(t, "", string(val3))

	db.Open()
	IsEmpty, err = db.IsEmpty()
	require.NoError(t, err)
	require.False(t, IsEmpty)

	db2 := db.(*PebbleDB).db
	batch := &PebbleBatch{
		dbName: "",
		batch:  db2.NewBatch(),
	}
	batch.batch.Set([]byte("key1"), []byte("value1"), pebble.NoSync)
	batch.batch.Set([]byte("key2"), []byte("value2"), pebble.NoSync)
	batch.batch.Delete([]byte("key3"), pebble.NoSync)
	require.NoError(t, db.WriteBatch(batch, true))

	val1, err1 = db.Get([]byte("key1"))
	require.NoError(t, err1, "")
	require.Equal(t, "value1", string(val1))

	val2, err2 = db.Get([]byte("key2"))
	require.NoError(t, err2, "")
	require.Equal(t, "value2", string(val2))

	val3, err3 = db.Get([]byte("key3"))
	require.NoError(t, err3, "")
	require.Equal(t, "", string(val3))

	var keys []string
	itr, err := db.GetIterator(nil, nil)
	require.NoError(t, err)
	for itr.Next() {
		keys = append(keys, string(itr.Key()))
	}
	itr.Release()
	require.Equal(t, []string{"key1", "key2"}, keys)
}

func TestFileLock(t *testing.T) {
	fileLockPath := testDBPath + "/fileLock"
	require.NoError(t, os.MkdirAll(testDBPath, 0o755))
	fileLock1 := NewFileLock(fileLockPath)
	require.NotNil(t, fileLock1)
	require.Equal(t, fileLock1.filePath, fileLockPath)

	err := fileLock1.Lock()
	require.NoError(t, err)

	fileLock2 := NewFileLock(fileLockPath)
	require.NotNil(t, fileLock2)
	require.Equal(t, fileLock2.filePath, fileLockPath)

	err = fileLock2.Lock()
	expectedErr := fmt.Sprintf("lock is already acquired on file %s", fileLockPath)
	require.EqualError(t, err, expectedErr)

	fileLock1.Unlock()

	err = fileLock2.Lock()
	require.NoError(t, err)

	fileLock2.Unlock()

	fileLock2.Unlock()

	require.NoError(t, os.RemoveAll(fileLockPath))
}

func TestFileLockLockUnlockLock(t *testing.T) {
	lockPath := testDBPath + "/fileLock"
	require.NoError(t, os.MkdirAll(testDBPath, 0o755))
	lock := NewFileLock(lockPath)
	require.NotNil(t, lock)
	require.Equal(t, lock.filePath, lockPath)
	require.False(t, lock.IsLocked())

	defer lock.Unlock()
	defer os.RemoveAll(lockPath)

	require.NoError(t, lock.Lock())
	require.True(t, lock.IsLocked())

	require.ErrorContains(t, lock.Lock(), "lock is already acquired")

	lock.Unlock()
	require.False(t, lock.IsLocked())

	require.NoError(t, lock.Lock())
	require.True(t, lock.IsLocked())
}

func TestCreateDBInEmptyDir(t *testing.T) {
	path := testDBPath + "/TestCreateDBInEmptyDir"
	require.NoError(t, os.RemoveAll(path), "")
	require.NoError(t, os.MkdirAll(path, 0o775), "")
	db := CreateDB(&Conf{DBPath: path})
	defer db.Close()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Panic is not expected when opening db in an existing empty dir. %s", r)
		}
	}()
	db.Open()
	require.NoError(t, os.RemoveAll(path))
}

func TestCreateDBInNonEmptyDir(t *testing.T) {
	path := testDBPath + "/TestCreateDBInNonEmptyDir"
	require.NoError(t, os.RemoveAll(path), "")
	require.NoError(t, os.MkdirAll(path, 0o775), "")
	file, err := os.Create(filepath.Join(path, "dummyfile.txt"))
	require.NoError(t, err, "")
	file.Close()
	db := CreateDB(&Conf{DBPath: path})
	defer db.Close()
	db.Open()
	require.NoError(t, os.RemoveAll(path))
}

func TestProvider(t *testing.T) {
	path := testDBPath + "/TestProvider"
	env := newTestProviderEnv(t, path)
	defer env.cleanup()

	provider := env.provider

	db1 := provider.GetDBHandle("testdb1")
	require.NotNil(t, db1)

	db2 := provider.GetDBHandle("testdb2")
	require.NotNil(t, db2)

	require.NoError(t, db1.Put([]byte("key1"), []byte("value1"), true))
	require.NoError(t, db2.Put([]byte("key2"), []byte("value2"), true))

	val, err := db1.Get([]byte("key1"))
	require.NoError(t, err)
	require.Equal(t, "value1", string(val))

	val, err = db2.Get([]byte("key2"))
	require.NoError(t, err)
	require.Equal(t, "value2", string(val))

	valKey2, err := db1.Get([]byte("key2"))
	require.NoError(t, err)
	require.Nil(t, valKey2)

	require.NoError(t, provider.Drop("testdb1"))
	valKey1, err := db1.Get([]byte("key1"))
	require.NoError(t, err)
	require.Nil(t, valKey1)

	val, err = db2.Get([]byte("key2"))
	require.NoError(t, err)
	require.Equal(t, "value2", string(val))
}

func TestProviderDataFormat(t *testing.T) {
	path := testDBPath + "/TestProviderDataFormat"
	require.NoError(t, os.RemoveAll(path), "")
	provider, err := NewProvider(&Conf{DBPath: path, ExpectedFormat: "100"})
	require.NoError(t, err)

	format, err := provider.GetDataFormat()
	require.NoError(t, err)
	require.Equal(t, "100", format)

	provider.Close()

	provider, err = NewProvider(&Conf{DBPath: path, ExpectedFormat: "100"})
	require.NoError(t, err)
	format, err = provider.GetDataFormat()
	require.NoError(t, err)
	require.Equal(t, "100", format)

	provider.Close()

	_, err = NewProvider(&Conf{DBPath: path, ExpectedFormat: "200"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected format")
	require.Contains(t, err.Error(), "pebble db at")
}

func TestRetrieveDataFormatInfo(t *testing.T) {
	path := testDBPath + "/TestRetrieveDataFormatInfo"
	require.NoError(t, os.RemoveAll(path), "")
	provider, err := NewProvider(&Conf{DBPath: path, ExpectedFormat: "100"})
	require.NoError(t, err)
	provider.Close()

	info, err := RetrieveDataFormatInfo(path)
	require.NoError(t, err)
	require.Equal(t, "100", info.FormatVerison)
	require.False(t, info.IsDBEmpty)

	db2 := info.IsDBEmpty
	require.False(t, db2)
}
