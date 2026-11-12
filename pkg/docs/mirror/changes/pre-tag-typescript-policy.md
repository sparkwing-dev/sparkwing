### Fixed

- **release:** Release previews now verify the published SDK policy and reject changed TypeScript SDK source under an existing version before creating a tag. Cuts that reuse exact-source CI run the same check.
