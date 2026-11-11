### Security

- **auth:** Keep credential history out of public authentication refusals
  Unknown and ended credentials return the same 401 envelope and refusal
  headers. Detailed reasons stay in private server audit logs; CLI profile
  replacement guidance remains available. Response timing is not guaranteed
  uniform because ended credentials require additional lookups.
