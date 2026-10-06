## Exit Status

### dbmigrator migrate

- `0` if the state was migrated, whether or not some channels were already in the
  target format and were left as they were
- `1` if an error occurs

### dbmigrator purge

- `0` if the run completed, whether or not `--confirm` was given
- `1` if an error occurs

## Example Usage

### dbmigrator migrate example

Here is an example of the `dbmigrator migrate` command. It moves the world state
of a peer from the flat LevelDB store into the patricia merkle trie store, for
every channel of the database.

```
dbmigrator migrate --source /var/hyperledger/production/ledgersData/stateLeveldb --target /var/hyperledger/production/ledgersData/stateLeveldbTrie --to leveldbtrie
```

The source database is left untouched, and what was written is moved into the
target only after the whole of it has been checked against what was migrated.

### dbmigrator purge example

Here is an example of the `dbmigrator purge` command. It removes the database a
state was migrated out of, once the database it was migrated into has been found
to hold the whole of it. Without `--confirm` the run says what it would remove
and removes nothing.

```
dbmigrator purge --source /var/hyperledger/production/ledgersData/stateLeveldb --target /var/hyperledger/production/ledgersData/stateLeveldbTrie --confirm
```

<a rel="license" href="http://creativecommons.org/licenses/by/4.0/"><img alt="Creative Commons License" style="border-width:0" src="https://i.creativecommons.org/l/by/4.0/88x31.png" /></a><br />This work is licensed under a <a rel="license" href="http://creativecommons.org/licenses/by/4.0/">Creative Commons Attribution 4.0 International License</a>.
