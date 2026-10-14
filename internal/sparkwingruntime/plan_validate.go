package sparkwingruntime

func SuggestClosest(target string, candidates []string) string {
	return closestMatch(target, candidates)
}

func closestMatch(want string, candidates []string) string {
	if want == "" || len(candidates) == 0 {
		return ""
	}
	bestDist := -1
	best := ""
	for _, c := range candidates {
		d := levenshtein(want, c)
		if bestDist < 0 || d < bestDist {
			bestDist = d
			best = c
		}
	}
	limit := max(2, longerLen(want, best)/3)
	if bestDist <= limit {
		return best
	}
	return ""
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	ar, br := []rune(a), []rune(b)
	la, lb := len(ar), len(br)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min3(
				curr[j-1]+1,
				prev[j]+1,
				prev[j-1]+cost,
			)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

func longerLen(a, b string) int {
	if len(a) > len(b) {
		return len(a)
	}
	return len(b)
}
