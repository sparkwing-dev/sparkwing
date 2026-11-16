### Changed

- **cloud:** Launch Cloud jobs that become ready together in one launcher cycle
  The launcher keeps claiming until the controller has no award for it, up to
  eight per cycle, instead of claiming one job per poll. In a four-way fan-out
  the last job was claimed about 13 s after the first; now all four start
  launching at once.
- **controller:** Answer repeated GitHub reads within one run request from the first answer
  `POST /api/v1/runs` resolved the same repository, ref, commit and
  installation tokens several times over while checking a fetched source.
  Identical reads and token requests in one intake now go to GitHub once, which
  shortens run creation without changing what is verified.
