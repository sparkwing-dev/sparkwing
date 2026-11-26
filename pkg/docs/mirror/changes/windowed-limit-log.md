### Changed

- **store (Breaking):** The schema baseline advances to version 44 to record windowed limit events, including team invitations and invitation emails, in one log. Schema 43 stores upgrade automatically. Older binaries refuse the upgraded store. See the [migration guide](docs/migrations/_unreleased.md#upgrade-the-store-in-place-to-schema-baseline-44)
