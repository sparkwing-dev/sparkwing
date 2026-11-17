### Fixed

- **local runner:** A local runner exits when its controller is gone
  A runner whose controller died kept retrying the controller's socket
  forever, so private and release homes left `sparkwing-runner-*` user units
  running long after their run ended. The runner now exits when its state home
  is deleted, and when no controller has answered on the home's socket for 10
  minutes. A controller replacement rebinds the socket within seconds, so it
  keeps reusing the live runner.
