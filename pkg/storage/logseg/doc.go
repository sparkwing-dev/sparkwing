// Package logseg stores one node attempt's log as numbered, compressed
// segment objects sealed by a manifest.
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
// Keys follow one layout, built by [AttemptPrefix]:
//
//	logs/r<days>/teams/<team>/runs/<run>/<node>/a<attempt>-g<gen>/seg-000001.ndjson.gz
//	logs/r<days>/teams/<team>/runs/<run>/<node>/a<attempt>-g<gen>/manifest.json
//
// The retention class leads the key so one object lifecycle rule per
// class expires every team's logs.
package logseg
