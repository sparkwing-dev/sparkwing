## Bound object presence requests

`POST /api/v1/data/has-many` requires a nonzero RFC3339 `deadline` alongside
`digests`. Send the caller's query deadline; a missing deadline returns `400`.
Both Go data clients require an explicit presence budget. The server stops
presence work at the earlier of the caller's deadline and its 30-second response
window. Requests that expire or are cancelled return an error without partial
presence results.
