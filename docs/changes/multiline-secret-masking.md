### Security

- **logs:** Mask individual lines of multi-line secrets and CR, LF and CRLF variants
  Raw and trimmed lines of at least four Unicode code points receive the same encoded masks as full secrets. Whitespace-only lines are skipped.
  Each eligible line from a run secret is masked anywhere in that run's logs. Common lines such as PEM BEGIN/END or `apiVersion: v1` show as `***` when they belong to a secret.
