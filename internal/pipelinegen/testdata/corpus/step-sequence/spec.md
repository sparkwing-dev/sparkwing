---
shape: step-sequence
expect: pass
entrypoint: GenStepSequence
---
A single release job made of three ordered stages inside one unit of
work: compile produces a version string, sign consumes that version, and
package consumes it too. The three stages share one workspace and must
not be split across separate jobs; sign and package must both read the
version compile computed rather than recomputing it.
