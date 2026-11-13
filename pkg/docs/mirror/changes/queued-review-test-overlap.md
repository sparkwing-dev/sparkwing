### Changed

- Queued landing runs review alongside affected Go tests after policy and fast checks pass. Review failures cancel in-flight tests, and landing still requires every check to pass on the unchanged tree. Each reviewer model call stops after 30 minutes, so a silent reviewer cannot hold the land slot for the combined job's longer deadline.
