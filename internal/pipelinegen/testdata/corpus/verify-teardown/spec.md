---
shape: verify-teardown
expect: pass
entrypoint: GenVerifyTeardown
---
An integration-test pipeline that starts a database container, does not
let the test job begin until that database is actually accepting
connections, then runs the suite with a 20 minute cap. The container
must be torn down whether the suite passes or fails, and also if the
database never becomes ready.
