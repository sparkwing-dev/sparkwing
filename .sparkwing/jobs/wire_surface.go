package jobs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

const (
	wireShapesSourcePath = "pkg/wingwire/testdata/shapes.json"
	apiSpecSourcePath    = "api/openapi.yaml"
	wingwireSourcePath   = "pkg/wingwire/wingwire.go"
)

type wireShapes struct {
	Types []wireShapeType `json:"types"`
}

type wireShapeType struct {
	Type   string           `json:"type"`
	Fields []wireShapeField `json:"fields"`
}

type wireShapeField struct {
	Name      string           `json:"name"`
	Kind      string           `json:"kind"`
	OmitEmpty bool             `json:"omitempty"`
	Fields    []wireShapeField `json:"fields"`
}

// safety: Identifier is spelled the way the changelog entry and its migration
// section must spell it, so a declaration can be matched to the cut it covers
// rather than to the surface in general.
type wireCut struct {
	Surface    string
	Identifier string
	Detail     string
}

func (c wireCut) describe() string {
	return strings.TrimSpace(c.Surface + " " + c.Identifier + " " + c.Detail)
}

func sortCuts(cuts []wireCut) {
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].describe() < cuts[j].describe() })
}

func describeCuts(cuts []wireCut) []string {
	out := make([]string, 0, len(cuts))
	for _, c := range cuts {
		out = append(out, c.describe())
	}
	return out
}

// safety: the snapshot is the released contract, so anything it named must
// still be there under the same kind; only new entries are free.
func wireShapeCuts(prevJSON, curJSON string) ([]wireCut, error) {
	prev, err := flattenWireShapes(prevJSON)
	if err != nil {
		return nil, fmt.Errorf("previous %s: %w", wireShapesSourcePath, err)
	}
	cur, err := flattenWireShapes(curJSON)
	if err != nil {
		return nil, fmt.Errorf("current %s: %w", wireShapesSourcePath, err)
	}
	var cuts []wireCut
	for path, kind := range prev {
		now, ok := cur[path]
		switch {
		case !ok:
			cuts = append(cuts, wireCut{Surface: "wingwire", Identifier: path, Detail: "removed"})
		case now != kind:
			cuts = append(cuts, wireCut{Surface: "wingwire", Identifier: path, Detail: "retyped " + kind + " -> " + now})
		}
	}
	sortCuts(cuts)
	return cuts, nil
}

func flattenWireShapes(body string) (map[string]string, error) {
	var shapes wireShapes
	if err := json.Unmarshal([]byte(body), &shapes); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, ty := range shapes.Types {
		out[ty.Type] = "message"
		flattenWireFields(out, ty.Type, ty.Fields)
	}
	return out, nil
}

func flattenWireFields(out map[string]string, prefix string, fields []wireShapeField) {
	for _, f := range fields {
		path := prefix + "." + f.Name
		kind := f.Kind
		if f.OmitEmpty {
			kind += ",omitempty"
		}
		out[path] = kind
		flattenWireFields(out, path, f.Fields)
	}
}

var specMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"patch": true, "head": true, "options": true, "trace": true,
}

// safety: a route or a response member a released controller served is part of
// the contract even when no Go type names it, so the inventory is read from the
// document rather than from the handler signatures.
func apiSurfaceCuts(prevSpec, curSpec string) ([]wireCut, error) {
	prev, err := apiSurface(prevSpec)
	if err != nil {
		return nil, fmt.Errorf("previous %s: %w", apiSpecSourcePath, err)
	}
	cur, err := apiSurface(curSpec)
	if err != nil {
		return nil, fmt.Errorf("current %s: %w", apiSpecSourcePath, err)
	}
	var cuts []wireCut
	for entry := range prev {
		if !cur[entry] {
			cuts = append(cuts, wireCut{Surface: "api", Identifier: entry, Detail: "removed"})
		}
	}
	sortCuts(cuts)
	return cuts, nil
}

func apiSurface(spec string) (map[string]bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(stripSpecHeader(spec)), &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("want a single YAML mapping document")
	}
	root := doc.Content[0]
	paths := specMapValue(root, "paths")
	// safety: an empty inventory would make every route look like growth, so a
	// document without a paths mapping fails the gate instead of passing it.
	if paths == nil || paths.Kind != yaml.MappingNode || len(paths.Content) == 0 {
		return nil, fmt.Errorf("no paths mapping")
	}
	out := map[string]bool{}
	for j := 0; j+1 < len(paths.Content); j += 2 {
		path, item := paths.Content[j].Value, paths.Content[j+1]
		out[path] = true
		if item.Kind != yaml.MappingNode {
			continue
		}
		collectSpecParameters(out, path, item)
		for k := 0; k+1 < len(item.Content); k += 2 {
			method := strings.ToLower(item.Content[k].Value)
			if !specMethods[method] {
				continue
			}
			operation := strings.ToUpper(method) + " " + path
			out[operation] = true
			collectSpecParameters(out, operation, item.Content[k+1])
		}
	}
	collectSpecProperties(out, "", root)
	return out, nil
}

func collectSpecParameters(out map[string]bool, prefix string, node *yaml.Node) {
	params := specMapValue(node, "parameters")
	if params == nil || params.Kind != yaml.SequenceNode {
		return
	}
	for _, p := range params.Content {
		if key := specNodeIdentity(p); key != "" {
			out[prefix+" parameter "+key] = true
		}
	}
}

func collectSpecProperties(out map[string]bool, path string, node *yaml.Node) {
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i].Value, node.Content[i+1]
			child := path + "." + key
			if key == "properties" && value.Kind == yaml.MappingNode {
				for j := 0; j+1 < len(value.Content); j += 2 {
					out[strings.TrimPrefix(child+"."+value.Content[j].Value, ".")] = true
				}
			}
			collectSpecProperties(out, child, value)
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			collectSpecProperties(out, path+"["+specSequenceKey(child, i)+"]", child)
		}
	}
}

// safety: a sequence entry keyed by position turns a reordered oneOf into a
// false cut, so an entry that identifies itself is keyed by that instead.
func specSequenceKey(node *yaml.Node, index int) string {
	if key := specNodeIdentity(node); key != "" {
		return key
	}
	if props := specMapValue(node, "properties"); props != nil && props.Kind == yaml.MappingNode {
		var names []string
		for i := 0; i+1 < len(props.Content); i += 2 {
			names = append(names, props.Content[i].Value)
		}
		sort.Strings(names)
		return strings.Join(names, "+")
	}
	return strconv.Itoa(index)
}

func specNodeIdentity(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.MappingNode {
		return ""
	}
	if ref := specScalar(node, "$ref"); ref != "" {
		return ref
	}
	if name := specScalar(node, "name"); name != "" {
		if in := specScalar(node, "in"); in != "" {
			return in + ":" + name
		}
		return name
	}
	return specScalar(node, "title")
}

func specMapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func specScalar(node *yaml.Node, key string) string {
	value := specMapValue(node, key)
	if value == nil || value.Kind != yaml.ScalarNode {
		return ""
	}
	return value.Value
}

func stripSpecHeader(spec string) string {
	lines := strings.Split(spec, "\n")
	i := 0
	for i < len(lines) && (strings.HasPrefix(lines[i], "#") || strings.TrimSpace(lines[i]) == "") {
		i++
	}
	return strings.Join(lines[i:], "\n")
}

var (
	protocolMajorRe    = regexp.MustCompile(`(?m)^const\s+ProtocolMajor\s*=\s*(\d+)\b`)
	minProtocolMajorRe = regexp.MustCompile(`(?m)^const\s+MinProtocolMajor\s*=\s*(\d+)\b`)
)

type protocolMajors struct {
	Newest int
	Floor  int
}

func parseProtocolMajors(goSource string) (protocolMajors, error) {
	newest, err := parseNamedInt(protocolMajorRe, goSource, "ProtocolMajor")
	if err != nil {
		return protocolMajors{}, err
	}
	floor, err := parseNamedInt(minProtocolMajorRe, goSource, "MinProtocolMajor")
	if err != nil {
		return protocolMajors{}, err
	}
	return protocolMajors{Newest: newest, Floor: floor}, nil
}

func parseNamedInt(re *regexp.Regexp, goSource, name string) (int, error) {
	m := re.FindStringSubmatch(goSource)
	if m == nil {
		return 0, fmt.Errorf("no `const %s = N` in %s", name, wingwireSourcePath)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, fmt.Errorf("parse %s %q: %w", name, m[1], err)
	}
	return n, nil
}

// safety: raising the floor drops every pin below it onto the standalone path,
// which is a cut whatever the message set does; a raised ProtocolMajor on its
// own only opens a new generation and is growth.
func protocolFloorCuts(prev, cur protocolMajors) []wireCut {
	if cur.Floor <= prev.Floor {
		return nil
	}
	return []wireCut{{
		Identifier: "protocol floor",
		Detail:     fmt.Sprintf("raised %d -> %d (newest major %d)", prev.Floor, cur.Floor, cur.Newest),
	}}
}

type wireSurface struct {
	path string
	diff func(prev, cur string) ([]wireCut, error)
}

type wireSurfaceState struct {
	surface wireSurface
	present bool
	prev    string
	cur     string
}

var wireSurfaces = []wireSurface{
	{path: wireShapesSourcePath, diff: wireShapeCuts},
	{path: apiSpecSourcePath, diff: apiSurfaceCuts},
	{path: wingwireSourcePath, diff: protocolSourceCuts},
}

// safety: a tag cut before a surface existed has nothing to diff, so its
// absence is growth rather than the removal of everything it never carried.
func wireCuts(states []wireSurfaceState) ([]wireCut, error) {
	var cuts []wireCut
	for _, st := range states {
		if !st.present {
			continue
		}
		found, err := st.surface.diff(st.prev, st.cur)
		if err != nil {
			return nil, err
		}
		cuts = append(cuts, found...)
	}
	return cuts, nil
}

func protocolSourceCuts(prevSrc, curSrc string) ([]wireCut, error) {
	prev, err := parseProtocolMajors(prevSrc)
	if err != nil {
		return nil, fmt.Errorf("previous %s: %w", wingwireSourcePath, err)
	}
	cur, err := parseProtocolMajors(curSrc)
	if err != nil {
		return nil, fmt.Errorf("current %s: %w", wingwireSourcePath, err)
	}
	return protocolFloorCuts(prev, cur), nil
}
