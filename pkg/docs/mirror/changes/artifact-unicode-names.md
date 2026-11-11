### Docs

- **artifacts:** Clarify byte-exact Unicode names and filesystem collisions
  NFC and NFD spellings are distinct artifacts. Downloads refuse a destination found
  to exist before publication; choose distinct `--dest` names when a filesystem treats the
  spellings as one path. Pipeline staging replaces colliding destination paths.
