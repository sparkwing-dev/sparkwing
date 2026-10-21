# Masking contract

`secrets.Masker` owns Go masking. TypeScript implements this contract against
`testdata/masking.json` and the generated differential cases.

Registration keeps raw secrets separate from derived patterns. Every nonempty
raw secret is protected at any length. Registering a spelling already present
as an encoding still expands that raw secret. Repetition, registration order,
and `Merge` do not change the resulting registry. Merge copies registrations
and expanded patterns without encoding derived patterns again.

A raw secret also derives nonblank newline-separated fragments, removing only
a trailing CR from each line. `RegisterList` additionally derives comma-
separated items trimmed with Unicode White_Space characters because only
arguments declared `[]string` have comma semantics. Ordinary secrets
containing commas stay whole. Every derived line or comma item must be at
least 12 UTF-8 bytes, matching the existing operator environment threshold.
Short items remain protected when explicitly registered as raw secrets. This
threshold keeps common words such as `value` from becoming incidental
patterns.

Each raw secret and eligible fragment derives JSON and Go quoted spellings,
URL query/path/component spellings, standard and URL-safe base64, and the
interior base64 characters independent of surrounding bytes at each alignment.
JSON/Go escapes are protected at any length. Other encoding patterns need four
bytes. Empty patterns are never registered. Derived patterns never recursively
derive more encodings.

Matching is literal, including a decoded JSON-escape view mapped back to the
original bytes. Enumerate every occurrence, including self-overlapping starts,
then redact the union of their byte ranges. Adjacent ranges may retain
separate markers. Escapes exposed by a replacement are masked too. A marker
containing a registered pattern uses an empty replacement, so even raw secrets
such as `*` cannot recur forever. Ordinary surrounding text survives. Chunked
streams retain enough raw overlap to cover occurrences across chunk
boundaries.

Arbitrary JSON data keys, string values, and scalar spellings use the complete
registry. Only fields carried by typed envelopes are exempt from key masking.
A key that merely resembles a schema field has no exemption. Masked key
collisions return the safe replacement marker for the whole object because an
object cannot preserve both values under one key. Objects are visited in
sorted key order. Malformed JSON is masked as text. Numeric values preserve
their exact spelling unless masked. Planning error codes containing a pattern
use the typed fallback code (`plan_failed` by default), while message, hint
and details are masked. Typed error details are omitted when a collision masks
the whole details object.

Shared vectors cover raw/derived order, merging, fragments, encodings, keys,
collisions, overlap and marker collisions. Generated cases compare Go and
TypeScript outputs for randomized registrations, interleavings and chunkings,
and independently check that protected raw secrets and fragments do not
survive.
