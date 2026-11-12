# Store fixtures

`Open` and `OpenPostgres` return ready stores. With PostgreSQL they clone a
process-owned initialized template into a separate database for each fixture.
When CREATE DATABASE is refused, they use the existing fresh-schema path.
SQLite fixture setup is unchanged.

`New` and `NewPostgres` reserve empty targets. Use them for fresh initialization,
schema upgrades, reopening and cases that need multiple schemas in one database.
The schema-lock guard checks that precondition before testing lock isolation.

A fixture package calls `storetest.Main` from TestMain. It runs the tests, drops
process-owned templates and performs the normal leak check. Per-test cleanup
closes stores before dropping their cloned databases. Template connections are
closed and further connections disabled before cloning.

The PostgreSQL job owns its disposable embedded cluster. On a configured server,
a process killed before teardown can leave `sw_fixture_clone_*` or
`sw_fixture_template_*` databases. Drop those only after their owner has exited.

The fixture strategy keeps existing test names, iterations and seeds. Runtime
measurements and outcome manifests live outside the repository in the ticket's
evidence directory.
