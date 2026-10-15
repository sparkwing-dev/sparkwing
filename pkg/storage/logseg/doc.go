// Package logseg stores job-attempt and owner-unit logs as numbered, compressed
// segments sealed by a manifest with one tagged subject.
//
// A [Sink] runs beside the node. It appends every record to a local
// spool file, cuts a segment every [SegmentInterval] or [SegmentBytes],
// whichever comes first, uploads it through an [Uploader], and writes
// the [Manifest] last, when the node finishes. A runner lost mid-node
// costs at most the segment being cut; the manifest the controller
// writes for the abandoned attempt then reads as incomplete.
//
// A reader opens a [Log] over any [Objects] store, serves its lines in
// order, verifies every segment against the manifest's digest, and
// reports a [Completeness] verdict.
//
// Keys are built by [SubjectPrefix]:
//
//	logs/r<days>/teams/<team>/runs/<run>/jobs/<job>/a<attempt>-g<gen>/
//	logs/r<days>/teams/<team>/runs/<run>/units/trigger/g<gen>/
//	logs/r<days>/teams/<team>/runs/<run>/units/plan/g<gen>/
//	logs/r<days>/teams/<team>/runs/<run>/units/eval/<source>/s<source-gen>-g<gen>/
//
// The retention class leads the key so one object lifecycle rule per
// class expires every team's logs.
package logseg
