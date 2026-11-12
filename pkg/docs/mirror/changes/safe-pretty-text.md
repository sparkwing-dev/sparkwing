### Security

- **cli:** Escape terminal controls and bidirectional formatting in displayed run, approval, annotation, log and pipeline text
  Human-readable summaries truncate UTF-8 text at rune boundaries. JSON records
  preserve their original values. Plain log streams keep their original text.
