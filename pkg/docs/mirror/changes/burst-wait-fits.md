### Fixed

- **orchestrator:** A burst job waiting for this machine no longer claims and returns itself every second
  A `--route burst` run whose job waits behind other work on the same machine used to take the job, be refused by the machine, and hand the job back about 49 times a minute, each time minting a job credential and calling the controller about 20 times. It happened whenever the machine had some CPU free but less than the job's charge, such as about 0.6 of a core on a 2-core machine running one job. The run now checks whether the job's whole charge fits and, while it does not, waits by asking this machine's local controller every 100 ms. The job stays ready for Cloud or another machine during the wait and starts as soon as the machine has room.
