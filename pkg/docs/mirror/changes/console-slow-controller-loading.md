### Fixed

- **web:** Keep console pages loading, rather than empty or stuck, while the controller is slow
  The runs list shows loading rows until its first response instead of "No runs yet". A request that outlives its deadline says the controller did not answer and may be busy, instead of "signal is aborted without reason", and no longer raises the "Cannot reach the sparkwing controller" banner. Home, Runs and Fleet wait for a pending refresh rather than sending another, and Home allows its overview read up to 60 seconds, so a slow answer still renders.
