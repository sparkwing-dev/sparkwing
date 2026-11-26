### Fixed

- **web:** Show each run's repository and pipeline in full on phones and tablets
  Below 1024 px the runs list cut every field on one line, so rows read like "spa… / pre-…". Narrow rows now
  wrap the repository, pipeline and branch in full and leave the commit, trigger, requester and agent to the run
  detail. A run value's filter menu also stays inside the screen when the value sits near its right edge.
