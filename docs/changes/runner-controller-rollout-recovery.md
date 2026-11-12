### Fixed

- **runner:** Keep claim recovery live through controller rollouts and longer outages
  Fresh claims retain capped backoff and cap Retry-After advice at five minutes
  per response while held work
  keeps its execution context and existing lease limits. Sustained ten-minute
  outages warn and make the optional readiness endpoint report degraded until
  recovery, including a successful claim accepted during shutdown.
  Terminal run acknowledgements also wait through temporary outages within
  their existing award context, including preparation-failure acknowledgements.
