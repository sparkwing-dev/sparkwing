### Security

- **controller + runner:** Refuse a malformed OTLP endpoint at startup
  An OTLP endpoint variable that is not an `http://` or `https://` URL with a
  host now stops the service or runner with an error naming the variable.
  The exporters used to fall back to `localhost:4318` and send
  `OTEL_EXPORTER_OTLP_HEADERS` there.
