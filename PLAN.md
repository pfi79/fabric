# План: Добавление PebbleDB как альтернативы GoLevelDB в Hyperledger Fabric

## Мотивация

PebbleDB (cockroachdb/pebble) — быстрая LSM-tree KV-база на Go, используется в
продакшене CockroachDB. Даёт значительный прирост производительности по
сравнению с GoLevelDB (syndtr/goleveldb) за счёт лучшей конкурентности,
современного движка compaction и оптимизированного слияния SST.

**Выбранная версия PebbleDB:** `v2.1.6` (последняя стабильная, 2026-05-27)

**Модель:** Пользователь конфигурирует `stateDatabase: pebbledb` и запускает
одноразовую миграцию через отдельную утилиту `dbmigrator`. Миграция —
однонаправленная (goleveldb → pebbledb), выполняется на остановленном узле.

---

## Текущая архитектура хранения данных

### Уровень 1: Низкоуровневая обёртка LevelDB

`common/ledger/util/leveldbhelper/`

Конкретные типы (без интерфейсов), напрямую используемые всеми потребителями:

| Тип | Импорт | Описание |
|-----|--------|----------|
| `Provider` | `*leveldbhelper.Provider` | Фабрика логических БД над одним физическим LevelDB |
| `DBHandle` | `*leveldbhelper.DBHandle` | Логическая БД (ключи с префиксом `dbName+0x00`) |
| `DB` | `*leveldbhelper.DB` | Обёртка над `*leveldb.DB` (Open/Close/Get/Put/...) |
| `UpdateBatch` | `*leveldbhelper.UpdateBatch` | Обёртка над `*leveldb.Batch` с префиксацией ключей |
| `Iterator` | `*leveldbhelper.Iterator` | Обёртка над `leveldb/iterator.Iterator` |
| `FileLock` | `*leveldbhelper.NewFileLock()` | Кросс-процессная блокировка через OpenFile LevelDB |

### Уровень 2: Потребители leveldbhelper

```
privacyenabledstate/db.go  (выбор: goleveldb / CouchDB)
  │
  ├── core/ledger/kvledger/txmgmt/statedb/stateleveldb/
  │     VersionedDBProvider (реализует statedb.VersionedDBProvider)
  │     versionedDB         (реализует statedb.VersionedDB)
  │     kvScanner, fullDBScanner
  │
  ├── common/ledger/blkstorage/
  │     BlockStoreProvider  (индекс блоков: blockNum → file pointer)
  │     Используется и peer, и orderer
  │
  ├── core/ledger/pvtdatastorage/
  │     Provider (приватные write-sets, expiry)
  │
  ├── core/ledger/kvledger/history/
  │     DBProvider (история значений по ключам)
  │
  ├── core/ledger/kvledger/bookkeeping/
  │     Provider (expiry-индексы, метаданные, снапшоты)
  │
  ├── core/ledger/confighistory/
  │     dbProvider (история конфигураций коллекций)
  │
  ├── core/transientstore/
  │     StoreProvider (временные RWsets после endorsement)
  │
  ├── core/ledger/kvledger/txmgmt/statedb/statecouchdb/
  │     redoLoggerProvider (redo-log для CouchDB)
  │
  └── core/ledger/kvledger/kv_ledger_provider.go
        idStore (метаданные ledger ID) — использует CreateDB() напрямую
        FileLock — блокировка через leveldb.OpenFile
        upgradeFormat() — использует leveldb.Batch напрямую
```

### Уровень 3: Orderer

Orderer использует LevelDB **единственным образом** — через `blkstorage` для
индекса блоков:

```
orderer/common/server/main.go
  → createLedgerFactory()
    → fileledger.New(location, metricsProvider)
      → blkstorage.NewProvider(conf, indexConfig, metricsProvider)
        → leveldbhelper.NewProvider(Conf{DBPath: location + "/index/"})
```

etcdraft хранит метаданные консенсуса в WAL/snap файлах (через
etcd `go.etcd.io/etcd/server/v3/storage/wal`), SmartBFT — в собственном
WAL (`github.com/hyperledger-labs/SmartBFT/pkg/wal`, файлы `*.wal`).
Оба **не используют LevelDB**.

---

## План изменений

### Этап 1: Версионирование [Done]

- `go get github.com/cockroachdb/pebble/v2@v2.1.6`
- `go mod tidy && go mod vendor`

### Этап 2: Интерфейсы — `common/ledger/util/db/` [Done]

Создать новый пакет с интерфейсами для всех низкоуровневых операций с БД.

**`common/ledger/util/db/interfaces.go`:**

```go
package db

// DB — низкоуровневое подключение к физической БД
type DB interface {
    Open()
    Close()
    Get(key []byte) ([]byte, error)
    Put(key, value []byte, sync bool) error
    Delete(key []byte, sync bool) error
    GetIterator(start, end []byte) (Iterator, error)
    IsEmpty() (bool, error)
    WriteBatch(b Batch, sync bool) error
}

// Provider — мульти-тенантная фабрика (логические БД с префиксацией)
type Provider interface {
    GetDBHandle(name string) DBHandle
    Drop(name string) error
    Close()
    GetDataFormat() (string, error)
    SetDataFormat(string) error
}

// DBHandle — handle к логической БД (ключи + префикс dbName+0x00)
type DBHandle interface {
    Get(key []byte) ([]byte, error)
    Put(key, value []byte, sync bool) error
    Delete(key []byte, sync bool) error
    GetIterator(start, end []byte) (Iterator, error)
    NewUpdateBatch() Batch
    WriteBatch(b Batch, sync bool) error
    IsEmpty() (bool, error)
}

// Batch — батч для атомарной записи
type Batch interface {
    Put(key, value []byte)
    Delete(key []byte)
    Len() int
    Reset()
}

// Iterator — итератор по диапазону ключей
type Iterator interface {
    Next() bool
    Key() []byte
    Value() []byte
    Error() error
    Release()
    Seek(key []byte) bool
}
```

### Этап 3: Рефакторинг `leveldbhelper` — имплементация интерфейсов [Done]

**`common/ledger/util/leveldbhelper/`:**

- Существующие типы становятся имплементациями `db.DB`, `db.Provider`,
  `db.DBHandle`, `db.Batch`, `db.Iterator`
- Пакет экспортирует конструкторы, возвращающие интерфейсы:
  ```go
  func NewProvider(conf *db.Conf) (db.Provider, error)
  func CreateDB(conf *db.Conf) db.DB
  func NewFileLock(filePath string) *FileLock  // остаётся отдельно
  ```
- Старые имена (`*Provider`, `*DBHandle`) остаются как алиасы (или удалить
  — потребители всё равно переходят на интерфейсы)

### Этап 4: PebbleDB helper — `common/ledger/util/pebblehelper/` [Done]

**Новый пакет**, имплементирующий те же интерфейсы `db.*` для PebbleDB.

**Файлы:**

| Файл | Содержание |
|------|-----------|
| `pebble_helper.go` | `PebbleDB` — имплементация `db.DB` (Open/Close/Get/Put/...) |
| `pebble_provider.go` | `PebbleProvider` — имплементация `db.Provider` (GetDBHandle/Drop) |
| `pebble_batch.go` | `PebbleBatch` — имплементация `db.Batch` |
| `pebble_iterator.go` | `PebbleIterator` — имплементация `db.Iterator` |
| `file_lock.go` | `FileLock` — через `syscall.Flock` на пустой файл |

**Отличия от LevelDB:**

- `pebble.Open()` НЕ захватывает файловую блокировку → FileLock отдельно
- `pebble.Iterator` не имеет `Error()` (возвращает ошибку через `pebble.Iterator.Error`)
- `pebble.Iterator.SeekGE()` / `SeekLT()` вместо единого `Seek()` — адаптировать
- `pebble.DB.WriteBatch()` заменяется на `batch.Commit(pebble.Sync)`
- Отдельные опции кэша и compaction

### Этап 5: StateDB на Pebble — `statepebbledb` [Done]

**`core/ledger/kvledger/txmgmt/statedb/statepebbledb/`** — по образу `stateleveldb/`.

**Файлы:**
- `statepebbledb.go` — `VersionedDBProvider` + `versionedDB`
  (реализуют `statedb.VersionedDBProvider` и `statedb.VersionedDB`)
- `statepebbledb_test.go` — unit-тесты
- `statepebbledb_test_export.go` — `TestVDBEnv` (как в stateleveldb)

**Ключевые отличия от stateleveldb:**
- Использует `pebblehelper` вместо `leveldbhelper`
- Итераторы (`kvScanner`, `fullDBScanner`) работают через `pebblehelper.Iterator`
- Кодирование/декодирование ключей и значений — идентично stateleveldb

### Этап 6: Переключатель state DB [Done]

**`core/ledger/ledger_interface.go`:**
```go
const (
    GoLevelDB = "goleveldb"
    PebbleDB  = "pebbledb"
    CouchDB   = "CouchDB"
)
```

**`core/ledger/kvledger/txmgmt/privacyenabledstate/db.go`** — `NewDBProvider`:
```go
case ledger.PebbleDB:
    vdbProvider, err = statepebbledb.NewVersionedDBProvider(stateDBConf.LevelDBPath)
```

### Этап 7: Конфигурация [Done]

**`sampleconfig/core.yaml`** (peer):
```yaml
ledger:
  state:
    # stateDatabase - options are "goleveldb", "pebbledb", "CouchDB"
    stateDatabase: goleveldb
  history:
    # stateDatabase - options are "goleveldb", "pebbledb"
    stateDatabase: goleveldb
```

**`sampleconfig/orderer.yaml`** (orderer):
```yaml
FileLedger:
  Location: /var/hyperledger/production/orderer
  # stateDatabase - options are "goleveldb", "pebbledb"
  stateDatabase: goleveldb
```

**`orderer/common/localconfig/config.go`** — структура `FileLedger`:
```go
type FileLedger struct {
    Location      string
    StateDatabase string  // новое
    Prefix        string  // deprecated
}
```

### Этап 8: Blkstorage — прокидывание типа БД [Done]

**`common/ledger/blkstorage/`**

`blkstorage.NewProvider()` принимает параметр `dbType string` и создаёт
соответствующий провайдер (`leveldbhelper` или `pebblehelper`) через фабрику:

```go
func NewProvider(conf *Conf, indexConfig *IndexConfig,
    metricsProvider metrics.Provider, dbType string) (*BlockStoreProvider, error) {

    var dbProvider db.Provider
    switch dbType {
    case "pebbledb":
        dbProvider, _ = pebblehelper.NewProvider(dbConf)
    default:
        dbProvider, _ = leveldbhelper.NewProvider(dbConf)
    }
    ...
}
```

**`common/ledger/blockledger/fileledger/factory.go`** — `New()` принимает `dbType`:

```go
func New(directory string, dbType string, metricsProvider metrics.Provider) (blockledger.Factory, error) {
    p, err := blkstorage.NewProvider(
        blkstorage.NewConf(directory, -1),
        &blkstorage.IndexConfig{AttrsToIndex: []blkstorage.IndexableAttr{blkstorage.IndexableAttrBlockNum}},
        metricsProvider,
        dbType,
    )
    ...
}
```

### Этап 9: Переход потребителей на интерфейсы [Done]

Все пакеты, которые сейчас импортируют конкретные типы `leveldbhelper`, меняют
их на интерфейсы из `db/`:

#### 9.1 blkstorage [Done]

`blockstore_provider.go`, `blockindex.go`, `blockfile_mgr.go`, `rollback.go`:
`*leveldbhelper.Provider` → `db.Provider`

#### 9.2 pvtdatastorage [Done]

`store.go`, `snapshot_data_importer.go`:
`*leveldbhelper.Provider` → `db.Provider`

#### 9.3 history [Done]

`db.go`:
`*leveldbhelper.Provider` → `db.Provider`

#### 9.4 bookkeeping [Done]

`provider.go`:
`*leveldbhelper.Provider` → `db.Provider`

#### 9.5 confighistory [Done]

`db_helper.go`:
`*leveldbhelper.Provider` → `db.Provider`

#### 9.6 transientstore [Done]

`store.go`:
`*leveldbhelper.Provider` → `db.Provider`

#### 9.7 statecouchdb [Done]

`redolog.go`:
`*leveldbhelper.Provider` → `db.Provider`

#### 9.8 stateleveldb [Done]

`stateleveldb.go`:
`*leveldbhelper.Provider` → `db.Provider`

#### 9.9 kvledger [Done]

`kv_ledger_provider.go`:
- idStore: `*leveldbhelper.DB` → `db.DB`
- FileLock: отдельно через `NewFileLock()`
- upgradeFormat: `leveldb.Batch` → `db.Batch`

---

### Этап 10: Переход оставшихся хранилищ на конфигурируемый тип БД

Все потребители ниже уже переведены на интерфейсы `db.*` (Этап 9), но **всегда
создают GoLevelDB**. Изменения: добавить параметр `dbType` в конструктор /
конфиг и переключаться между `leveldbhelper.NewProvider` и
`pebblehelper.NewProvider`.

#### 10.1 bookkeeping [Done]

**`core/ledger/kvledger/bookkeeping/provider.go`:**

```go
// Было:
func NewProvider(path string) *Provider

// Стало:
func NewProvider(path, dbType string) (*Provider, error)
```

- `NewProvider` принимает `dbType`; коммутатор по типу БД.
- **Вызывающая сторона:** `kv_ledger_provider.go:initStateDBProvider()` —
  передавать `dbType` из конфига.

#### 10.2 transientstore [Done]

**`core/transientstore/store.go`:**

```go
// Было:
func NewStoreProvider(path string) (*StoreProvider, error)

// Стало:
func NewStoreProvider(path, dbType string) (*StoreProvider, error)
```

- `newStoreProvider` — коммутатор по `dbType`.
- `storeProvider.fileLock` → `*leveldbhelper.FileLock` заменить на интерфейс
  (или принимать `dbType` и выбирать `leveldbhelper.NewFileLock`
  / `pebblehelper.NewFileLock`).
- **Вызывающая сторона:** `kv_ledger_provider.go` — передавать `dbType` из конфига.

#### 10.3 pvtdatastorage [Done]

**Конфиг (`core/ledger/pvtdatastorage/store.go`):**

```go
type PrivateDataConfig struct {
    PrivateDataConfig *ledger.PrivateDataConfig
    StorePath         string
    DBType            string   // новое поле
}
```

- `NewProvider(conf *PrivateDataConfig)` — коммутатор по `conf.DBType`.
- **Зависимые файлы:**
  - `snapshot_data_importer.go:newSnapshotRowsSorter()` (строка 402) —
    создаёт временную LevelDB для сортировки. Можно заменить на
    `pebblehelper` или оставить как есть (временная БД, удаляется после
    импорта). **Рекомендация:** оставить LevelDB — временная БД не влияет
    на производительность.
  - `retroactive_hashed_index.go:constructHashedIndex()` (строка 50) —
    утилита однократного апгрейда формата данных. Работает на
    остановленном узле до переключения на PebbleDB. **Изменений не
    требует.**
- **Вызывающая сторона:** `kv_ledger_provider.go:initPvtDataStoreProvider()`
  — заполнять `DBType` из конфига.

#### 10.4 statecouchdb/redolog [Done]

**`core/ledger/kvledger/txmgmt/statedb/statecouchdb/redolog.go`:**

```go
// Было:
func newRedoLoggerProvider(dirPath string) (*redoLoggerProvider, error)

// Стало:
func newRedoLoggerProvider(dirPath, dbType string) (*redoLoggerProvider, error)
```

- Коммутатор по `dbType` в `newRedoLoggerProvider`.

#### 10.5 kv_ledger_provider — FileLock [Done]

**`core/ledger/kvledger/kv_ledger_provider.go`:**

Сейчас FileLock создаётся через `leveldbhelper.NewFileLock()` — конкретный тип
`*leveldbhelper.FileLock` в структуре `Provider` (строка 70) и в вызовах
(строки 94-95).

**Изменения:**

```go
// Определить интерфейс:
type FileLock interface {
    Lock() error
    Unlock() error
}

// Provider.fileLock: *leveldbhelper.FileLock → FileLock

// Конструктор FileLock по типу БД:
func newFileLock(path, dbType string) (FileLock, error) {
    switch dbType {
    case "pebbledb":
        return pebblehelper.NewFileLock(filePath)
    default:
        return leveldbhelper.NewFileLock(filePath)
    }
}
```

- `leveldbhelper.NewFileLock(path)` остаётся без изменений.
- `pebblehelper.NewFileLock(path)` — новая имплементация через
  `syscall.Flock` на пустой файл (как в `pebblehelper/file_lock.go`).

#### 10.6 Вспомогательные утилиты (upgrade/reset/rebuild/rollback)

**Файлы в `core/ledger/kvledger/`:**

| Файл | Использование | Изменение |
|------|--------------|-----------|
| `upgrade_dbs.go` | `leveldbhelper.CreateDB` | Принимать `dbType`, коммутатор |
| `reset.go` | `leveldbhelper.CreateDB` | Принимать `dbType`, коммутатор |
| `rebuild_dbs.go` | `leveldbhelper.CreateDB` | Принимать `dbType`, коммутатор |
| `pause_resume.go` | `leveldbhelper` | Принимать `dbType`, коммутатор |
| `rollback.go` | `leveldbhelper` | Принимать `dbType`, коммутатор |
| `unjoin_channel.go` | `leveldbhelper` | Принимать `dbType`, коммутатор |
| `common/ledger/blkstorage/rollback.go` | `leveldbhelper.NewProvider` (строка 75) | Принимать `dbType`, коммутатор |

Для всех: заменить прямые вызовы `leveldbhelper.CreateDB` на фабричный
метод, принимающий `dbType`. Предлагается создать:

```go
// common/ledger/util/db/factory.go
func CreateDB(conf *Conf, dbType string) (DB, error) {
    switch dbType {
    case PebbleDB:
        return pebblehelper.CreateDB(conf)
    default:
        return leveldbhelper.CreateDB(conf)
    }
}

func NewProvider(conf *Conf, dbType string) (Provider, error) {
    switch dbType {
    case PebbleDB:
        return pebblehelper.NewProvider(conf)
    default:
        return leveldbhelper.NewProvider(conf)
    }
}
```

Тогда все потребители вызывают единую фабрику `db.CreateDB(conf, dbType)`
вместо ручного свитчинга.

#### 10.7 Конфигурация: peer [Done]

**`core/ledger/kvledger/kv_ledger_provider.go` — `Provider`:**

Добавить поле `dbType string`, заполняемое из конфига. Все
`init*Provider()` методы получают `dbType` и передают его соответствующим
конструкторам.

**`core/ledger/kvledger/kv_ledger_provider.go` — изменения в init-методах:**

```go
func (p *Provider) initStateDBProvider() {
    dbType := p.dbType  // из конфига
    stateDBProvider, err = bookkeeping.NewProvider(path, dbType)
    ...
    vdbProvider, err := privacyenabledstate.NewDBProvider(...)
    // privacyenabledstate.DBProvider сам переключает stateleveldb
    // на основе переданного dbType
}

func (p *Provider) initPvtDataStoreProvider() {
    conf := &pvtdatastorage.PrivateDataConfig{
        StorePath: p.privateDataConfig.StorePath,
        DBType:    p.dbType,
    }
    pvtDataStoreProvider, err = pvtdatastorage.NewProvider(conf)
}

func (p *Provider) initTransientStoreProvider() {
    p.transientStoreProvider, err = transientstore.NewStoreProvider(path, p.dbType)
}
```

Для History, ConfigHistory и BlockStore — уже сделано (передают `dbType`
из конфига).

#### 10.8 Обновление конфигурации [Done]

**`sampleconfig/core.yaml`:**
```yaml
ledger:
  state:
    stateDatabase: goleveldb   # уже есть
  history:
    stateDatabase: goleveldb   # удалить, использовать параметр internalDatabases
  # Новые поля — единый тип БД для всех вспомогательных хранилищ:
  internalDatabases:
    stateDatabase: goleveldb   # bookkeeper, pvtdata, transient, redo-log
```

Либо упрощённый подход — один параметр `stateDatabase` для всех хранилищ
(кроме блок-индекса orderer, который конфигурируется отдельно).

**Рекомендация:** добавить единую опцию:

```yaml
ledger:
  stateDatabase: goleveldb   # общий тип для ВСЕХ LevelDB-хранилищ
  state:
    stateDatabase: goleveldb   # только state DB (если нужен отдельный)
```

Тогда пользователь конфигурирует один раз, а history/state могут
переопределяться.

#### 10.9 Обновлённая конфигурация orderer [Done]

**`sampleconfig/orderer.yaml`:**
```yaml
FileLedger:
  Location: /var/hyperledger/production/orderer
  stateDatabase: goleveldb   # уже есть (для blkstorage index)
```

Дополнительных полей не требуется — orderer использует LevelDB только
для индекса блоков, и оно уже конфигурируется.

---

### Этап 11: Утилита миграции — `cmd/dbmigrator`

**Отдельная standalone-программа**, собирается как `build/bin/dbmigrator`.

**Makefile:**
```makefile
TOOLS_EXES = configtxgen configtxlator cryptogen discover ledgerutil osnadmin peer dbmigrator
pkgmap.dbmigrator := $(PKGNAME)/cmd/dbmigrator
```

**CLI (kingpin, как ledgerutil):**
```
dbmigrator --db-path=<path> [--db-path=<path>...]
```

**Help выводит список типичных LevelDB-директорий для миграции:**

Для peer:
```
ledgersData/stateLeveldb
ledgersData/historyLeveldb
ledgersData/bookkeeper
ledgersData/pvtdataStore
ledgersData/configHistory
ledgersData/chains/<channelId>/index
transientStore
```

Для orderer:
```
chains/<channelId>/index
```

**Алгоритм работы (`cmd/dbmigrator/main.go`):**

1. Парсинг флагов — принимает один или несколько `--db-path`
2. Пользователь самостоятельно останавливает узел (peer/orderer)
3. Для каждой переданной директории:
   - Открыть как LevelDB (через `leveldbhelper`)
   - Создать новую директорию `{dir}_pebble`
   - Открыть как PebbleDB (через `pebblehelper`)
   - Итерировать LevelDB → писать батчи в PebbleDB (по 1MB)
   - Прогресс-бар (счётчик ключей/байт)
4. По завершению: вывести инструкцию:
   ```
   Migration complete. To activate PebbleDB:
   1. Backup old directories:
      mv ledgersData/stateLeveldb ledgersData/stateLeveldb.bak
   2. Rename migrated directories:
      mv ledgersData/stateLeveldb_pebble ledgersData/stateLeveldb
   3. Update config:
      core.yaml:   ledger.state.stateDatabase: pebbledb
      orderer.yaml: FileLedger.stateDatabase: pebbledb
   4. Start the node
   ```

**Файлы утилиты:**

| Файл | Назначение |
|------|-----------|
| `cmd/dbmigrator/main.go` | CLI, парсинг аргументов |
| `cmd/dbmigrator/migrate.go` | Логика миграции: открыть source → открыть target → копировать батчами |

**Ограничения:**
- Обратной миграции нет (нужен бекап вручную)
- Выполняется на остановленном узле
- Не мигрирует WAL/snap консенсуса (etcdraft, smartbft)

---

### Этап 12: Интеграционные тесты

**12.1 Тест миграции хранилищ (Ginkgo + Gomega)**

- Набор тестов `core/ledger/kvledger/txmgmt/statedb/statepebbledb/integration/`
- Поднимает тестовую сеть через `nwo`
- Развёртывает network с `stateDatabase: goleveldb`
- Деплоит chaincode, отправляет транзакции
- Останавливает сеть
- Запускает `dbmigrator` для миграции LevelDB → PebbleDB
- Меняет конфиг на `stateDatabase: pebbledb`
- Перезапускает сеть
- Верифицирует: чтение старых данных, выполнение новых транзакций,
  история, приватные данные
- Аналогичный тест для orderer (fileledger index migration)

**12.2 Бенчмарк производительности хранилищ**

- Тест `core/ledger/kvledger/txmgmt/statedb/statepebbledb/benchmark_test.go`
- Сравнение GoLevelDB vs PebbleDB на одинаковых сценариях:
  - Sequential write (put 1M keys)
  - Random read
  - Range scan (итерация)
  - Batch write
- Замеры: ops/sec, latency p50/p99, размер БД на диске
- Запуск: `go test -bench=. -benchtime=1x ./core/ledger/kvledger/txmgmt/statedb/statepebbledb/...`
- Результаты сохранять в `docs/benchmark/pebble_vs_goleveldb.md`

---

## Файловая структура новых/изменённых пакетов

```
common/ledger/util/
├── db/
│   └── interfaces.go                         [НОВЫЙ]
├── leveldbhelper/
│   ├── leveldb_helper.go                     [ИЗМЕНЁН]
│   ├── leveldb_provider.go                   [ИЗМЕНЁН]
│   └── leveldb_helper_test.go                [ИЗМЕНЁН]
└── pebblehelper/
    ├── pebble_helper.go                      [НОВЫЙ]
    ├── pebble_provider.go                    [НОВЫЙ]
    ├── pebble_batch.go                       [НОВЫЙ]
    ├── pebble_iterator.go                    [НОВЫЙ]
    ├── file_lock.go                          [НОВЫЙ]
    └── pebble_helper_test.go                 [НОВЫЙ]

core/ledger/kvledger/txmgmt/statedb/
├── statedb.go                                [без изменений]
└── statepebbledb/
    ├── statepebbledb.go                      [НОВЫЙ]
    ├── statepebbledb_test.go                 [НОВЫЙ]
    └── statepebbledb_test_export.go          [НОВЫЙ]

common/ledger/blkstorage/
├── blockstore_provider.go                    [ИЗМЕНЁН]
├── blockindex.go                             [ИЗМЕНЁН]
├── blockfile_mgr.go                          [ИЗМЕНЁН]
├── rollback.go                               [ИЗМЕНЁН]
└── config.go                                 [без изменений]

common/ledger/blockledger/fileledger/
├── factory.go                                [ИЗМЕНЁН]
└── ...                                       [без изменений]

core/ledger/
├── ledger_interface.go                       [ИЗМЕНЁН]
├── pvtdatastorage/store.go                   [ИЗМЕНЁН]
├── kvledger/history/db.go                    [ИЗМЕНЁН]
├── kvledger/bookkeeping/provider.go           [ИЗМЕНЁН]
├── kvledger/confighistory/db_helper.go       [ИЗМЕНЁН]
├── kvledger/kv_ledger_provider.go            [ИЗМЕНЁН]
└── kvledger/txmgmt/privacyenabledstate/db.go [ИЗМЕНЁН]

core/transientstore/store.go                  [ИЗМЕНЁН]

orderer/common/localconfig/config.go          [ИЗМЕНЁН]

cmd/dbmigrator/
├── main.go                                   [НОВЫЙ]
└── migrate.go                                [НОВЫЙ]

sampleconfig/
├── core.yaml                                 [ИЗМЕНЁН]
└── orderer.yaml                              [ИЗМЕНЁН]

Makefile                                       [ИЗМЕНЁН]
```

---

## Итого: статистика изменений

| Категория | Новые файлы | Изменённые файлы |
|-----------|-------------|------------------|
| Интерфейсы | 1 | 0 |
| LevelDB helper | 0 | 2 |
| PebbleDB helper | 6 | 0 |
| StatePebbleDB | 3 | 0 |
| Blkstorage | 0 | 4 |
| FileLedger | 0 | 1 |
| Потребители (8 пакетов) | 0 | ~10 |
| Фабрика `db/factory.go` | 1 | 0 |
| bookkeeping | 0 | 1 |
| transientstore | 0 | 1 |
| pvtdatastorage | 0 | 3 |
| statecouchdb/redolog | 0 | 1 |
| kvledger (filelock) | 0 | 1 |
| Вспомогательные утилиты | 0 | ~6 |
| Orderer config | 0 | 1 |
| Утилита миграции | 2 | 0 |
| Конфиг | 0 | 2 |
| Makefile | 0 | 1 |
| **Всего** | **~13** | **~34** |

---

## Риски и открытые вопросы

### FileLock
Сейчас FileLock = открыть LevelDB → LevelDB держит flock на директории. Pebble не
делает этого. Решение: отдельная реализация через `syscall.Flock` на пустой файл
в `pebblehelper/file_lock.go`.

### Совместимость форматов ключей
LevelDB и PebbleDB используют разные on-disk форматы. Данные копируются
побайтово через итератор — это корректно, так как кодирование ключей/значений
задаётся Fabric-кодом (префиксы `dbName+0x00`, `'d'` для data-ключей,
`protobuf` для значений), а не библиотекой.

### Пороговая уверенность для корректности данных
- Батчи атомарны в обоих БД
- Итерация LevelDB → запись в PebbleDB батчами с periodic sync
- После завершения — fsync директории

### Производительность миграции
Для больших сторов (>100GB) миграция может занимать часы. Предлагаемое
решение:
- Батчи по 1MB
- Progress bar через отслеживание количества ключей
- Resumable: если прервалось — удалить partial PebbleDB, запустить заново

### Что НЕ входит в план
- Поддержка `pebbledb` в integration test framework (`nwo`)
- Docker-образы с PebbleDB (используется тот же бинарник `peer`/`orderer`)
- Миграция WAL/snap консенсуса (etcdraft, smartbft)
- Обратная миграция `pebbledb` → `goleveldb`
