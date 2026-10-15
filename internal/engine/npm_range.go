package engine

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

type npmBound struct {
	version             string
	parts               int
	major, minor, patch uint64
}

func parseNpmBound(raw string) (npmBound, bool) {
	raw = strings.TrimPrefix(raw, "v")
	if raw == "" || raw == "*" || strings.EqualFold(raw, "x") {
		return npmBound{version: "v0.0.0"}, true
	}
	suffix := ""
	if index := strings.IndexAny(raw, "-+"); index >= 0 {
		suffix = raw[index:]
		raw = raw[:index]
	}
	fields := strings.Split(raw, ".")
	if len(fields) > 3 {
		return npmBound{}, false
	}
	bound := npmBound{}
	values := []*uint64{&bound.major, &bound.minor, &bound.patch}
	wildcard := false
	for i, field := range fields {
		if field == "*" || strings.EqualFold(field, "x") {
			wildcard = true
			continue
		}
		if wildcard || field == "" || (len(field) > 1 && field[0] == '0') {
			return npmBound{}, false
		}
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return npmBound{}, false
		}
		*values[i] = value
		bound.parts++
	}
	if suffix != "" && bound.parts != 3 {
		return npmBound{}, false
	}
	bound.version = fmt.Sprintf("v%d.%d.%d%s", bound.major, bound.minor, bound.patch, suffix)
	return bound, semver.IsValid(bound.version)
}
func (b npmBound) upper(part int) string {
	switch part {
	case 1:
		return fmt.Sprintf("v%d.0.0", b.major+1)
	case 2:
		return fmt.Sprintf("v%d.%d.0", b.major, b.minor+1)
	default:
		return fmt.Sprintf("v%d.%d.%d", b.major, b.minor, b.patch+1)
	}
}
func npmSatisfies(spec, version string) bool {
	actual := "v" + version
	if !releaseVersion(actual) {
		return false
	}
	for _, alternative := range strings.Split(spec, "||") {
		if npmAlternative(strings.TrimSpace(alternative), actual) {
			return true
		}
	}
	return false
}
func npmAlternative(spec, actual string) bool {
	if spec == "latest" {
		return semver.Prerelease(actual) == ""
	}
	fields := strings.Fields(spec)
	if len(fields) == 0 {
		return false
	}
	prereleaseAllowed := semver.Prerelease(actual) == ""
	if len(fields) == 3 && fields[1] == "-" {
		lower, ok := parseNpmBound(fields[0])
		if !ok {
			return false
		}
		upper, ok := parseNpmBound(fields[2])
		if !ok {
			return false
		}
		prereleaseAllowed = prereleaseAllowed || samePrereleaseBase(actual, lower.version) || samePrereleaseBase(actual, upper.version)
		if !prereleaseAllowed || semver.Compare(actual, lower.version) < 0 {
			return false
		}
		if upper.parts < 3 {
			return upper.parts == 0 || semver.Compare(actual, upper.upper(upper.parts)) < 0
		}
		return semver.Compare(actual, upper.version) <= 0
	}
	for i := 0; i < len(fields); i++ {
		token := fields[i]
		if strings.Trim(token, "<>=~^") == "" && i+1 < len(fields) {
			i++
			token += fields[i]
		}
		operator := ""
		for len(token) > 0 && strings.ContainsRune("<>=~^", rune(token[0])) {
			operator += token[:1]
			token = token[1:]
		}
		bound, ok := parseNpmBound(token)
		if !ok {
			return false
		}
		prereleaseAllowed = prereleaseAllowed || samePrereleaseBase(actual, bound.version)
		if !npmCompare(actual, operator, bound) {
			return false
		}
	}
	return prereleaseAllowed
}
func samePrereleaseBase(a, b string) bool {
	return semver.Prerelease(b) != "" && strings.SplitN(a, "-", 2)[0] == strings.SplitN(b, "-", 2)[0]
}
func npmCompare(actual, operator string, b npmBound) bool {
	compare := semver.Compare(actual, b.version)
	switch operator {
	case "", "=":
		if b.parts == 0 {
			return true
		}
		if b.parts == 3 {
			return compare == 0
		}
		return compare >= 0 && semver.Compare(actual, b.upper(b.parts)) < 0
	case ">=":
		return compare >= 0
	case ">":
		if b.parts == 0 {
			return false
		}
		if b.parts < 3 {
			return semver.Compare(actual, b.upper(b.parts)) >= 0
		}
		return compare > 0
	case "<":
		return b.parts > 0 && compare < 0
	case "<=":
		if b.parts == 0 {
			return true
		}
		if b.parts < 3 {
			return semver.Compare(actual, b.upper(b.parts)) < 0
		}
		return compare <= 0
	case "~":
		if b.parts == 0 {
			return true
		}
		part := 2
		if b.parts == 1 {
			part = 1
		}
		return compare >= 0 && semver.Compare(actual, b.upper(part)) < 0
	case "^":
		if b.parts == 0 {
			return true
		}
		part := 1
		if b.major == 0 && b.parts > 1 {
			part = 2
			if b.minor == 0 && b.parts > 2 {
				part = 3
			}
		}
		return compare >= 0 && semver.Compare(actual, b.upper(part)) < 0
	default:
		return false
	}
}
