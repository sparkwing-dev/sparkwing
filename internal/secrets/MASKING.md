# Host masking contract

The Go host owns one `secrets.Masker`. It registers each accepted secret value,
including each normalized list item, before author code can release it. Host
issued values are registered before a service returns them. `Register` and
`Validate` refuse values shorter than four Unicode code points. Refusal applies
to stored secrets, supplied secret arguments and nonempty secret defaults of the selected pipeline. Each nonempty list item participates.
An empty default carries no value. Use a plain config variable for values that
fail this rule. Invalid derived proxy parts are skipped. Unselected defaults do
not refuse the selected pipeline.

Each accepted value is matched literally and through its JSON/Go quoted, URL
query/path/component, standard and URL-safe base64, and embedded base64
spellings. Percent escape hex digits match regardless of case. Encoded forms
are not recursively derived. The marker is `***`. Overlapping matches merge,
and replacements repeat to mask spellings formed by marker edges. Secrets
containing `***` are accepted under decision 49; replacement can form one across
already published emissions, so replacement closure excludes those secrets.
JSON escape views cover serialized attributes whose string values themselves contain
escaped spellings.

`Stream.Mask` accepts consecutive raw byte chunks before line splitting and
returns bytes safe to publish. Compiled Aho-Corasick matchers scan raw bytes and
JSON escape views. Only suffixes that could complete a spelling remain private.
`Stream.Flush` returns the final tail at EOF. The host flushes safe partial lines
after 25 ms of idle output without releasing an unfinished pattern.
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
