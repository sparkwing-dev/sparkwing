package match

import (
	"fmt"
	"slices"
	"strings"
)

// CloudSize binds a runner label to its physical allocation and billed class.
type CloudSize struct {
	Label       string
	CreditClass int64
	Resources   Resources
}

// CloudSizeMemoryBytes reserves the node overhead before assigning memory to a job.
func CloudSizeMemoryBytes(cores int64) int64 {
	usable := cores*(4<<30)*37/40 - (2 << 30)
	return max(usable&^(1<<20-1), 0)
}

// CloudSizes returns the fixed Cloud catalog in ascending credit-class order.
func CloudSizes() []CloudSize {
	out := make([]CloudSize, 0, 6)
	for _, cores := range []int64{2, 4, 8, 16, 32, 64} {
		out = append(out, CloudSize{Label: fmt.Sprintf("%s%d%s", CloudSizeLabelPrefix, cores, CloudSizeLabelSuffix), CreditClass: cores, Resources: Resources{Cores: float64(cores), MemoryBytes: CloudSizeMemoryBytes(cores)}})
	}
	return out
}

// CloudSizeForProfile resolves size terms using the same AND/OR rules as selectors.
// Terms without a Cloud size label remain the claim module's responsibility.
func CloudSizeForProfile(selector []string, profile Profile) (CloudSize, error) {
	profile.Labels = slices.DeleteFunc(slices.Clone(profile.Labels), IsCloudSizeLabel)
	for _, size := range CloudSizes() {
		candidate := profile
		candidate.Labels = append(slices.Clone(profile.Labels), size.Label)
		matched := true
		for _, term := range selector {
			sizeTerm := false
			for _, alt := range strings.Split(term, ",") {
				sizeTerm = sizeTerm || IsCloudSizeLabel(strings.TrimSpace(alt))
			}
			if sizeTerm && !TermSatisfied(candidate, term) {
				matched = false
				break
			}
		}
		if matched {
			return size, nil
		}
	}

	return CloudSize{}, fmt.Errorf("no Cloud size satisfies required labels %q", selector)
}

// CloudSizeLabelPrefix and CloudSizeLabelSuffix bound every Cloud size label, sparkwing-<cores>cpu. The store's claim
// query spells IsCloudSizeLabel in SQL from these two values.
const (
	CloudSizeLabelPrefix = "sparkwing-"
	CloudSizeLabelSuffix = "cpu"
)

// IsCloudSizeLabel reports whether label names a Cloud size.
func IsCloudSizeLabel(label string) bool {
	return strings.HasPrefix(label, CloudSizeLabelPrefix) && strings.HasSuffix(label, CloudSizeLabelSuffix)
}

// ValidateCloudSizeLabels refuses reserved size labels outside the Cloud catalog.
// Comma-separated alternatives follow the selector grammar.
func ValidateCloudSizeLabels(selector []string) error {
	sizes := CloudSizes()
	labels := make([]string, len(sizes))
	for i, size := range sizes {
		labels[i] = size.Label
	}
	for _, term := range selector {
		for _, alt := range strings.Split(term, ",") {
			label := strings.TrimSpace(alt)
			if IsCloudSizeLabel(label) && !slices.Contains(labels, label) {
				return fmt.Errorf("unsupported Cloud size %q; valid sizes: %s", label, strings.Join(labels, ", "))
			}
		}
	}
	return nil
}
