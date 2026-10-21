# Host masking contract

The Go host owns one `secrets.Masker`. It registers each accepted secret value,
including each normalized list item, before author code can release it. Host
issued values are registered before a service returns them. `Register` and
`Validate` refuse values shorter than four Unicode code points or containing
`***`; the caller should use a plain config variable for those values.

Each accepted value is matched literally and through its JSON/Go quoted, URL
query/path/component, standard and URL-safe base64, and embedded base64
spellings. Percent escape hex digits match regardless of case. Encoded forms
are not recursively derived. The marker is `***`. Overlapping matches merge,
and replacements repeat until no registered spelling survives. JSON escape
views cover serialized attributes whose string values themselves contain
escaped spellings.

`Stream.Mask` accepts consecutive raw byte chunks before line splitting and
returns bytes safe to publish. `Stream.Flush` returns the final tail at EOF.
Each stream has one goroutine owner. Registration can add values while a stream
exists; bytes already emitted cannot be recalled. The host therefore registers
known values before spawning author code and lazily issued values before
releasing them.

`Attributes` masks serialized JSON using the same matcher, then requires a
single JSON object with unique keys at every level. If the result is invalid,
contains duplicate keys, or changes shape, it returns
`{"masked_attrs":"***"}`. Trusted protocol envelopes are constructed after
payload masking, so their field names are never subject to matching.

Pipeline code run without a Sparkwing host has no automatic masking.
