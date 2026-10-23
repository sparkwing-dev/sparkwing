# Host masking contract

The Go host owns one `secrets.Masker`. Secret values shorter than four Unicode
code points are accepted, encrypted and access-controlled like other secrets,
but `Register` skips them before deriving any masking patterns. This applies to
stored and local secrets, supplied secret arguments, defaults and normalized
list items. CLI writes and local-secret intake warn; API write and intake
responses include a `warning` field; console forms show an inline note. The
host warns when SDK arguments or defaults contain short values, including
those discovered by generators. No warning includes the secret's value.
Empty supplied arguments and defaults carry no value.

For values of at least four code points, the host registers each accepted value,
including each normalized list item, before author code can release it. Host
issued values are registered before a service returns them. Derived proxy parts
shorter than four code points are skipped as well.

Each registered value is matched literally and through its JSON/Go quoted, URL
query/path/component, standard and URL-safe base64, and embedded base64
spellings. Percent escape hex digits match regardless of case. Encoded forms
are not recursively derived. The marker is `***`. Overlapping matches merge,
and replacement closure uses at most four passes. If ambiguity remains, the
remaining payload is replaced as a whole. Secrets containing `***` are accepted
under decision 49. Replacement can form a registered value containing `***`,
or a value containing `*` beside a replacement marker, across already published
emissions. For example, a published `aab` followed by a marker forms `aab*`.
Decision 49 accepts this residual. Replacement closure excludes these values,
and stream and batch masking may differ at these marker edges.
JSON escape views cover serialized attributes whose string values themselves contain
escaped spellings.

`Stream.Mask` accepts consecutive raw byte chunks before line splitting and
returns bytes safe to publish. Compiled Aho-Corasick matchers scan raw bytes and
JSON escape views. Sparse automata are added per registration without rebuilding
the existing registry. Only suffixes that could complete a spelling remain private,
bounded by the longest spelling. Completed redaction ranges are emitted.
`Stream.Flush` returns the final tail at EOF. The host flushes safe partial lines
after 25 ms of idle output without releasing an unfinished pattern.
Each stream has one goroutine owner. Registration can add values while a stream
exists; bytes already emitted cannot be recalled. The host therefore registers
known values before spawning author code and lazily issued values before
releasing them.

Fresh identifiers are checked against every registered mask, including issued
values. Issuance does not invalidate identifiers already accepted. That exemption
applies only to exact replay of accepted topology. New generated job IDs and
expansions receive the full check.

`Attributes` masks serialized JSON using the same matcher, then requires a
single JSON object with unique keys at every level. If the result is invalid,
contains duplicate keys, or changes shape, it returns
`{"masked_attrs":"***"}`. Trusted protocol envelopes are constructed after
payload masking, so their field names are never subject to matching. Size and
idle flushes preserve complete UTF-8 boundaries.

Pipeline code run without a Sparkwing host has no automatic masking.
