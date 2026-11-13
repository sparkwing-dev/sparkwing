### Fixed

- **ci:** Run repository-scanning guards for unrelated Go test changes
  Affected local and hosted tests run the named source guards even when the
  changed test belongs to another module. Documentation-only selection stays
  unchanged.
