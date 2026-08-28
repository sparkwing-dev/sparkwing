package sparkwingruntime

import "sort"

func SortedUniqueRisks(slices ...[]string) []string {
	seen := map[string]bool{}
	for _, sl := range slices {
		for _, l := range sl {
			if l == "" || seen[l] {
				continue
			}
			seen[l] = true
		}
	}
	out := make([]string, 0, len(seen))
	for l := range seen {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}
