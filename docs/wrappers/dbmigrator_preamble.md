# dbmigrator

The `dbmigrator` command moves the world state of a peer from one of the two
shapes it can be kept in into the other: the flat store of LevelDB, and the store
that keeps the state of a channel in a patricia merkle trie over the same
LevelDB.

The database that is handed to a migration is opened for reading and for nothing
else, and is not written to at any point of a run. What is written goes into a
directory beside the database that is being written and is given the name of that
database only after the whole of it has been checked against what was migrated.

## Syntax

The `dbmigrator` command has the following subcommands:

  * migrate
  * purge
