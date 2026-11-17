### Fixed

- **local runner:** A local runner exits when its controller is gone
  A runner whose controller died kept retrying the controller's socket
  forever, so private and release homes left `sparkwing-runner-*` user units
  running long after their run ended. The runner now exits once no controller
  has accepted a connection on the home's socket for 10 minutes, or for 1 minute
  when its state home is also missing on every check. A controller replacement
  rebinds the socket within seconds, so it keeps reusing the live runner. When
  the runner exits, its service stops and kills the jobs it still holds.
