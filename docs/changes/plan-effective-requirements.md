### Fixed

- **Pipeline plan:** Apply effective checkout requirements and reject unsupported Cloud sizes before previewing jobs

### Security

- **Pipeline plan:** Refuse effective runner labels containing registered secrets before preview output
- **Runner labels:** Refuse registered secrets in checkout requirements, preferences and profiles, including overridden declarations, before accepting run or generated-job settings or copying a retry
