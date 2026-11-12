### Fixed

- **sdk:** Windows TypeScript SDK verification now compares protocol pipe objects, so reused handle numbers do not cause false leak failures. The SDK and new TypeScript scaffolds move to 0.72.2 because the verification inputs changed.
