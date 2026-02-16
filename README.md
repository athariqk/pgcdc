# pgcdc

This is a PostgreSQL CDC written in Go for propagating changeset to any "connectors" (currently NSQ).

## Requirements

Requires WAL level set to logical. In postgresql.conf file:
```
wal_level = logical
```

## Usage

pgcdc requires a `schema.yaml` file to define how pgcdc should replicate the database.

The typical `schema.yaml` file looks like the following:

```yaml
nodes:
  table_1:
    namespace: "table's schema name"
    pk: <table's primary key>
    capture: "none" | "all"
    columns:
      - <column 1>
      - <column 2>
      ...
  table_2:
    columns:
      - <column 1>
      - <column 2>
      ...
    children:
      child_table_1:
        columns:
          - <column 1>
          - <column 2>
          ...
        transform:
          rename:
            old_column: "<new column>"
        relationship:
          type: "one_to_one" | "one_to_many"
          fk:
            child: <this node's primary key>
            parent: <parent's foreign key>
      child_table_2:
        pk: <table's primary key>
        columns:
          - <column 1>
          - <column 2>
          ...
```

### `nodes`

An object node describing the source table. Root-level tables lives here.

### `children`

An optional list of child nodes if any. This has the same structure as a parent node. Also defines relationship.

### `capture`

Specifies CDC behavior (defaults to all).

- `none`: only capture table in full replication mode
- `all`: capture and propagate all changeset

### `columns`

An optional list of table columns to capture (defaults to all). Note that the primary key will always be included even if it's not specified in the list.

### `relationship`

Describes the relationship between parent and child.

- `type`: type can be `one_to_one` or `one_to_many` depending on the relationship type between parent and child
- `fk`: specifies the foreign keys of the relationship

### `transform`

List of table-level transform operators.

- `rename`: renames `old_column` name to the specified `new_column` name

## Command-line Arguments

### stream

Run pgcdc in logical streaming replication protocol, this is the default replication/capture mode.

### bootstrap

Fully replicates/captures the schema to the connectors. Useful when you're trying to add missing documents or running CDC for the first time and need to "populate" the target data store.
