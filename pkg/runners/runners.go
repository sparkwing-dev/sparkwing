// Package runners defines the runner-pool schema carried under the
// runners: section of .sparkwing/sparkwing.yaml. It owns the types and
// their validation; the file is read by pkg/projectconfig, which
// normalizes and validates each section.
package runners

import (
	"fmt"
)

// File is the shape of the runners: section (a map keyed by name).
type File struct {
	Runners map[string]Runner `yaml:"runners"`
}

// Runner is one named entry under runners:. Name is populated from
// the map key during load; it is not read off the wire.
type Runner struct {
	Name string `yaml:"-"`

	// Type is the runner kind. Valid values:
	//   "local"      -- in-process, on whichever host runs the CLI or controller
	//   "kubernetes" -- pod materialized by a Kubernetes runner pool
	//   "static"     -- long-lived runner that registers itself
	Type string `yaml:"type"`

	// Profile is the named profile from profiles.yaml that hosts this
	// runner. Required for type=="kubernetes"; ignored for "local" and
	// "static". (Renamed from controller: in v0.5.0 -- the field always
	// referenced a profile by name.)
	Profile string `yaml:"profile,omitempty"`

	// Labels are the equality strings the runner advertises. Empty
	// strings inside the slice are silently dropped, matching how
	// Job.Requires filters its argument list.
	Labels []string `yaml:"labels,omitempty"`

	// Spec carries Kubernetes-only materialization details. Setting
	// it on a non-kubernetes entry is a validation error.
	Spec Spec `yaml:"spec,omitempty"`
}

// Spec is the kubernetes-only spec block: pod placement and
// resources. Other runner types ignore it.
type Spec struct {
	NodeSelector map[string]string `yaml:"nodeSelector,omitempty"`
	Tolerations  []Toleration      `yaml:"tolerations,omitempty"`
	Resources    Resources         `yaml:"resources,omitempty"`
}

// Toleration mirrors corev1.Toleration. The orchestrator stamps these
// onto the runner pod spec at dispatch.
type Toleration struct {
	Key      string `yaml:"key,omitempty"`
	Operator string `yaml:"operator,omitempty"`
	Value    string `yaml:"value,omitempty"`
	Effect   string `yaml:"effect,omitempty"`
}

// Resources mirrors corev1.ResourceRequirements with string values
// (so "2", "2Gi", "1000m" round-trip without quantity parsing here).
type Resources struct {
	Requests map[string]string `yaml:"requests,omitempty"`
	Limits   map[string]string `yaml:"limits,omitempty"`
}

// Validate checks every entry's structural invariants: Type is one
// of the documented values, Kubernetes runners declare a Profile,
// and non-kubernetes runners do not carry a Spec block.
func (f *File) Validate() error {
	for name, r := range f.Runners {
		switch r.Type {
		case "local", "kubernetes", "static":
			// ok
		case "":
			return fmt.Errorf("runner %q: type is required (one of: local, kubernetes, static)", name)
		default:
			return fmt.Errorf("runner %q: unknown type %q (valid: local, kubernetes, static)", name, r.Type)
		}
		if r.Type == "kubernetes" && r.Profile == "" {
			return fmt.Errorf("runner %q: type=kubernetes requires a profile field", name)
		}
		if r.Type != "kubernetes" && !specIsZero(r.Spec) {
			return fmt.Errorf("runner %q: spec block is only valid for type=kubernetes", name)
		}
	}
	return nil
}

func specIsZero(s Spec) bool {
	return len(s.NodeSelector) == 0 &&
		len(s.Tolerations) == 0 &&
		len(s.Resources.Requests) == 0 &&
		len(s.Resources.Limits) == 0
}
