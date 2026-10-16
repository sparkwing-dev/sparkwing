package userconfig

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// ParseByteSize parses the local cache byte bound; zero disables it.
func ParseByteSize(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	units := []struct {
		suffix string
		mult   int64
	}{
		{"KIB", 1 << 10},
		{"MIB", 1 << 20},
		{"GIB", 1 << 30},
		{"TIB", 1 << 40},
		{"KB", 1000},
		{"MB", 1000 * 1000},
		{"GB", 1000 * 1000 * 1000},
		{"TB", 1000 * 1000 * 1000 * 1000},
		{"B", 1},
	}
	upper := strings.ToUpper(s)
	for _, u := range units {
		if !strings.HasSuffix(upper, u.suffix) {
			continue
		}
		num := strings.TrimSpace(upper[:len(upper)-len(u.suffix)])
		_, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return 0, fmt.Errorf("parse size %q: %w", raw, err)
		}
		n, ok := new(big.Rat).SetString(num)
		if !ok {
			return 0, fmt.Errorf("parse size %q: invalid number", raw)
		}
		n.Mul(n, new(big.Rat).SetInt64(u.mult))
		if n.Sign() < 0 || (n.Sign() > 0 && n.Cmp(big.NewRat(1, 1)) < 0) || n.Cmp(new(big.Rat).SetInt64(math.MaxInt64)) > 0 {
			return 0, fmt.Errorf("parse size %q: outside byte range", raw)
		}
		return new(big.Int).Quo(n.Num(), n.Denom()).Int64(), nil
	}
	n, err := strconv.ParseInt(upper, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse size %q: %w", raw, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("parse size %q: negative", raw)
	}
	return n, nil
}
