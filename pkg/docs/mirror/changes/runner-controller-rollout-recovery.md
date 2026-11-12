### Fixed

- **runner:** Keep claim recovery live through controller rollouts and longer outages
  Fresh claims retain capped backoff and Retry-After advice while held work
  keeps its execution context and existing lease limits. Sustained ten-minute
  outages warn and make the optional health endpoint report degraded until recovery.
  Terminal run acknowledgements also wait through temporary outages within
  their existing award context, including preparation-failure acknowledgements.
