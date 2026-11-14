### Added

- **api:** Team administrators manage run event subscriptions
  `/api/v1/subscriptions` and `sparkwing subscriptions` create, list, show,
  delete and rotate the signing secret of a team subscription. A subscription
  names an HTTPS URL, the `run.started` and `run.finished` events it wants, and
  optional status, pipeline and repository filters. Sparkwing generates the
  `whsec_` signing secret, shows it once and stores it sealed to the team and
  subscription. A controller accepts only HTTPS URLs whose host is a public
  name or address; the local controller also accepts `http` and loopback hosts.
  Events are CloudEvents 1.0 JSON with type `dev.sparkwing.run.started.v1` or
  `dev.sparkwing.run.finished.v1`.

### Changed

- **store (Breaking):** The schema baseline advances to version 39 to store run event subscriptions and deliveries. Schema 38 stores upgrade automatically. Older binaries refuse the upgraded store. See the [migration guide](docs/migrations/_unreleased.md#upgrade-the-store-in-place-to-schema-baseline-39)
