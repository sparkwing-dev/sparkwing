### Security

- **controller:** Audit requests refused for another team's resources
  An authenticated request refused because the run, job, claim, child or team
  it names is missing or outside the credential's team or binding now writes an
  `access.refused` audit event in the caller's own team. The event records the
  actor, method, route template, refusal class and credential kind, never the
  request path, body or the identifiers it named. Each credential, route and
  class keeps at most one event per hour, and each team at most 300 per hour;
  the rest are counted in one `access.refused_suppressed` event. Responses are
  unchanged: a refused request still answers exactly like a missing resource.
