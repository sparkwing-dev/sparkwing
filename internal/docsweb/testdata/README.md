# Renderer conformance cases

`renderer-cases.json` is copied byte-for-byte from Sparkwing's
`internal/docsweb/testdata/renderer-cases.json`. Edit the Sparkwing source, then
copy the whole file to each consumer. Keep expected HTML hand-authored.
The tests exercise constructs because shipped pages can omit broken syntax.

The `requires` field selects applicable cases:

- `common`: headings through level three, paragraphs, dash lists, fences,
  escaping, inline code, bold and links.
- `wrapped`: list-item continuation. Sparkwing, Xwing, Pulsewing and Stormwing.
- `full`: ordered and star lists, deeper headings, tables and comment regions.
  Sparkwing. Bitwing has these features but adoption is tracked separately.
- `reduced`: literal numeric markers, pipes and comments. All five smaller renderers.
- `reduced-wrapped`: a pipe line continuing a list item. Xwing, Pulsewing and Stormwing.
- `authority-rejection`: normalized offsite authority rejection. Sparkwing,
  Xwing, Pulsewing and Stormwing.

Dowing and Sidewing do not implement list continuation or normalized
authority rejection. Those cases stay outside their supported profile. This
corpus preserves renderer behavior, including Sparkwing's lone-pipe table.
Renderer-specific page-link resolution stays in each repo's own tests.

Consumers are Sparkwing (`internal/docsweb`) and Xwing, Pulsewing, Stormwing,
Dowing and Sidewing (`internal/docs`). Archived Workwing and Flockwing are excluded.
Overwing's Notion and performance renderers have different contracts.

After copying, check each consumer against the Sparkwing source:

```sh
cmp /absolute/sparkwing/internal/docsweb/testdata/renderer-cases.json \
    /absolute/consumer/internal/docs/testdata/renderer-cases.json
```

Run the package tests in each changed repo. A copy update includes the same
case file in every consumer commit. No sibling checkout is needed to run tests.
