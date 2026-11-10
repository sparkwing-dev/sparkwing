### Fixed

- **cli:** Runs and triggers accept omitted optional secret arguments
  Supplied values remain masked. Missing required arguments are refused before
  submission with an error naming the flag. Invalid secret classifications
  receive HTTP 400 instead of HTTP 500.
