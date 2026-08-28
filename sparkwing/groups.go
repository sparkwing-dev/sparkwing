package sparkwing

import (
	"errors"
	"fmt"
	"strings"
)

type groupKind int

const (
	groupKindUnset groupKind = iota
	groupKindExactlyOne
	groupKindAtLeastOne
	groupKindAtMostOne
	groupKindAllOrNone
)

func (k groupKind) String() string {
	switch k {
	case groupKindExactlyOne:
		return "exactly one"
	case groupKindAtLeastOne:
		return "at least one"
	case groupKindAtMostOne:
		return "at most one"
	case groupKindAllOrNone:
		return "all or none"
	default:
		return "<unset>"
	}
}

type groupMeta struct {
	kind   groupKind
	fields []string
	when   Predicate
	desc   string
}

// GroupBuilder is the chainable handle returned by SchemaBuilder.Group.
// Methods set the group's kind, an optional When predicate that
// gates activation, and an optional Desc override for the violation
// message. SchemaBuilder.Build rejects groups whose kind was never set.
//
// Typical shape:
//
//	s.Group("Image", "ImageRef", "ImageDigest").ExactlyOne()
//	s.Group("AwsAccessKey", "AwsSecretKey").AllOrNone()
//	s.Group("X", "Y").AtLeastOne().When(sparkwing.ArgEq("mode", "deploy")).Desc("provide X or Y when mode=deploy")
type GroupBuilder struct {
	meta *groupMeta
}

func newGroupBuilder(fields []string) *GroupBuilder {
	cp := make([]string, len(fields))
	copy(cp, fields)
	return &GroupBuilder{meta: &groupMeta{fields: cp}}
}

// ExactlyOne requires exactly one of the group's fields to be set
// after resolution. Mutex + at-least-one combined.
func (g *GroupBuilder) ExactlyOne() *GroupBuilder {
	g.meta.kind = groupKindExactlyOne
	return g
}

// AtLeastOne requires at least one of the group's fields to be set.
// No upper bound.
func (g *GroupBuilder) AtLeastOne() *GroupBuilder {
	g.meta.kind = groupKindAtLeastOne
	return g
}

// AtMostOne forbids more than one of the group's fields being set.
// Zero is OK; two or more is a violation. Use for mutually-exclusive
// optional flags.
func (g *GroupBuilder) AtMostOne() *GroupBuilder {
	g.meta.kind = groupKindAtMostOne
	return g
}

// AllOrNone requires the group's fields to be uniformly set or
// uniformly unset. Useful for paired credentials (access-key +
// secret-key) where supplying one without the other is always wrong.
func (g *GroupBuilder) AllOrNone() *GroupBuilder {
	g.meta.kind = groupKindAllOrNone
	return g
}

// When gates the group's activation. The constraint is only checked
// when the predicate evaluates true; otherwise the group is dormant.
// Use to scope groups to specific resolution contexts ("at-most-one
// of these credentials when running remote").
func (g *GroupBuilder) When(p Predicate) *GroupBuilder {
	g.meta.when = p
	return g
}

// Desc overrides the auto-generated violation message. The default
// names the group's fields and the expected cardinality; supply a
// human-friendly message when the auto string would be confusing.
func (g *GroupBuilder) Desc(msg string) *GroupBuilder {
	g.meta.desc = msg
	return g
}

func evalGroup(g *groupMeta, ctx PredicateContext) error {
	if g.kind == groupKindUnset {
		return fmt.Errorf("sparkwing: group has no cardinality set (call ExactlyOne / AtLeastOne / AtMostOne / AllOrNone before SchemaBuilder.Build)")
	}
	if g.when != nil && !g.when.Eval(ctx) {
		return nil
	}
	setFields := make([]string, 0, len(g.fields))
	for _, name := range g.fields {
		if _, ok := ctx.Arg(name); ok {
			setFields = append(setFields, name)
		}
	}
	switch g.kind {
	case groupKindExactlyOne:
		if len(setFields) != 1 {
			return groupViolation(g, setFields)
		}
	case groupKindAtLeastOne:
		if len(setFields) == 0 {
			return groupViolation(g, setFields)
		}
	case groupKindAtMostOne:
		if len(setFields) > 1 {
			return groupViolation(g, setFields)
		}
	case groupKindAllOrNone:
		if len(setFields) > 0 && len(setFields) < len(g.fields) {
			return groupViolation(g, setFields)
		}
	}
	return nil
}

func groupViolation(g *groupMeta, setFields []string) error {
	if g.desc != "" {
		return errors.New(g.desc)
	}
	return fmt.Errorf("expected %s of [%s] to be set; got %d set: [%s]",
		g.kind.String(),
		strings.Join(g.fields, ","),
		len(setFields),
		strings.Join(setFields, ","),
	)
}
