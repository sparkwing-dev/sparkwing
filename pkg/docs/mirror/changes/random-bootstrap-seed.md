### Security

- **controller:** Reject constant-byte bootstrap administrator seeds
  Startup refuses placeholders such as an all-zero payload and shows the
  random-token generator. Bootstrap seeds must contain 32 cryptographically
  random bytes.
