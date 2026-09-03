package wingwire

import (
	"encoding/json"
	"flag"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

var updateShapes = flag.Bool("update", false, "rewrite testdata/shapes.json from the registered message set")

const shapesPath = "testdata/shapes.json"

const regenerateShapes = "GOWORK=off go test ./pkg/wingwire -run TestWireShapes -update"

type wireShapes struct {
	Types []wireType `json:"types"`
}

type wireType struct {
	Type   string      `json:"type"`
	Fields []wireField `json:"fields"`
}

type wireField struct {
	Name      string      `json:"name"`
	Kind      string      `json:"kind"`
	OmitEmpty bool        `json:"omitempty"`
	Fields    []wireField `json:"fields,omitempty"`
}

func TestWireShapes(t *testing.T) {
	got := marshalShapes(t, currentShapes(t))
	if *updateShapes {
		if err := os.WriteFile(shapesPath, got, 0o600); err != nil {
			t.Fatalf("write %s: %v", shapesPath, err)
		}
		return
	}
	want, err := os.ReadFile(shapesPath)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate with `%s`)", shapesPath, err, regenerateShapes)
	}
	if string(got) == string(want) {
		return
	}
	var committed wireShapes
	if err := json.Unmarshal(want, &committed); err != nil {
		t.Fatalf("parse %s: %v", shapesPath, err)
	}
	removed, retyped, added := compareShapes(flattenShapes(committed), flattenShapes(currentShapes(t)))
	t.Errorf("the wire surface no longer matches %s:\n%s\nbreaks -- removed: %s\nbreaks -- retyped: %s\ngrowth -- added: %s\n\nregenerate with `%s`, and declare any break in the changelog",
		shapesPath, diffLines(string(want), string(got)),
		formatShapeList(removed), formatShapeList(retyped), formatShapeList(added), regenerateShapes)
}

func TestRegistryAgreesWithEveryMessageType(t *testing.T) {
	for _, mt := range registeredTypes() {
		msg, err := emptyMessage(mt)
		if err != nil {
			t.Fatalf("emptyMessage(%q): %v", mt, err)
		}
		if got := msg.wireType(); got != mt {
			t.Errorf("registry builds %T under %q but the message calls itself %q", msg, mt, got)
		}
	}
}

func TestSnapshotCoversTheWholeRegistry(t *testing.T) {
	snapshot := map[MessageType]bool{}
	for _, mt := range snapshotMessageTypes(t) {
		snapshot[mt] = true
	}
	for _, mt := range registeredTypes() {
		if !snapshot[mt] {
			t.Errorf("message type %q is registered but absent from %s; regenerate with `%s`", mt, shapesPath, regenerateShapes)
		}
	}
	if len(snapshot) != len(registeredTypes()) {
		t.Errorf("%s carries %d types and the registry holds %d", shapesPath, len(snapshot), len(registeredTypes()))
	}
}

func TestCompareShapesSeparatesBreaksFromGrowth(t *testing.T) {
	old := map[string]string{
		"grant":          "message",
		"grant.run_id":   "string",
		"grant.cores":    "float64",
		"grant.lease":    "string,omitempty",
		"evicted":        "message",
		"evicted.run_id": "string",
	}
	cur := map[string]string{
		"grant":            "message",
		"grant.run_id":     "string",
		"grant.cores":      "string",
		"grant.lease":      "string",
		"grant.semaphores": "[]string,omitempty",
	}
	removed, retyped, added := compareShapes(old, cur)
	if want := []string{"evicted", "evicted.run_id"}; !reflect.DeepEqual(removed, want) {
		t.Errorf("removed = %v, want %v", removed, want)
	}
	if want := []string{"grant.cores (float64 -> string)", "grant.lease (string,omitempty -> string)"}; !reflect.DeepEqual(retyped, want) {
		t.Errorf("retyped = %v, want %v", retyped, want)
	}
	if want := []string{"grant.semaphores"}; !reflect.DeepEqual(added, want) {
		t.Errorf("added = %v, want %v", added, want)
	}
}

func TestRenderKindNamesTheKindNotTheGoType(t *testing.T) {
	fields := renderFields(reflect.TypeFor[AdmissionRequest]())
	kinds := map[string]string{}
	for _, f := range fields {
		kinds[f.Name] = f.Kind
	}
	if got := kinds["origin"]; got != "string" {
		t.Errorf("origin kind = %q, want string: a named string type renames without moving the wire", got)
	}
	if got := kinds["semaphores"]; got != "[]struct" {
		t.Errorf("semaphores kind = %q, want []struct", got)
	}
}

func snapshotMessageTypes(t *testing.T) []MessageType {
	t.Helper()
	body, err := os.ReadFile(shapesPath)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate with `%s`)", shapesPath, err, regenerateShapes)
	}
	var shapes wireShapes
	if err := json.Unmarshal(body, &shapes); err != nil {
		t.Fatalf("parse %s: %v", shapesPath, err)
	}
	out := make([]MessageType, 0, len(shapes.Types))
	for _, ty := range shapes.Types {
		out = append(out, MessageType(ty.Type))
	}
	return out
}

func currentShapes(t *testing.T) wireShapes {
	t.Helper()
	var shapes wireShapes
	for _, mt := range registeredTypes() {
		msg, err := emptyMessage(mt)
		if err != nil {
			t.Fatalf("emptyMessage(%q): %v", mt, err)
		}
		shapes.Types = append(shapes.Types, wireType{
			Type:   string(mt),
			Fields: renderFields(reflect.TypeOf(msg).Elem()),
		})
	}
	sort.Slice(shapes.Types, func(i, j int) bool { return shapes.Types[i].Type < shapes.Types[j].Type })
	return shapes
}

func marshalShapes(t *testing.T, shapes wireShapes) []byte {
	t.Helper()
	body, err := json.MarshalIndent(shapes, "", "  ")
	if err != nil {
		t.Fatalf("marshal shapes: %v", err)
	}
	return append(body, '\n')
}

func renderFields(t reflect.Type) []wireField {
	var out []wireField
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
			out = append(out, renderFields(f.Type)...)
			continue
		}
		if name == "" {
			name = f.Name
		}
		kind, nested := renderKind(f.Type)
		out = append(out, wireField{
			Name:      name,
			Kind:      kind,
			OmitEmpty: hasOption(opts, "omitempty"),
			Fields:    nested,
		})
	}
	return out
}

func hasOption(opts, want string) bool {
	for opt := range strings.SplitSeq(opts, ",") {
		if opt == want {
			return true
		}
	}
	return false
}

func renderKind(t reflect.Type) (kind string, fields []wireField) {
	if t == reflect.TypeFor[time.Duration]() {
		return "time.Duration", nil
	}
	switch t.Kind() {
	case reflect.Pointer:
		elem, nested := renderKind(t.Elem())
		return "*" + elem, nested
	case reflect.Slice, reflect.Array:
		elem, nested := renderKind(t.Elem())
		return "[]" + elem, nested
	case reflect.Map:
		key, _ := renderKind(t.Key())
		elem, nested := renderKind(t.Elem())
		return "map[" + key + "]" + elem, nested
	case reflect.Struct:
		return "struct", renderFields(t)
	default:
		return t.Kind().String(), nil
	}
}

func flattenShapes(s wireShapes) map[string]string {
	out := map[string]string{}
	for _, ty := range s.Types {
		out[ty.Type] = "message"
		flattenFields(out, ty.Type, ty.Fields)
	}
	return out
}

func flattenFields(out map[string]string, prefix string, fields []wireField) {
	for _, f := range fields {
		path := prefix + "." + f.Name
		kind := f.Kind
		if f.OmitEmpty {
			kind += ",omitempty"
		}
		out[path] = kind
		flattenFields(out, path, f.Fields)
	}
}

func compareShapes(old, cur map[string]string) (removed, retyped, added []string) {
	for path, kind := range old {
		switch now, ok := cur[path]; {
		case !ok:
			removed = append(removed, path)
		case now != kind:
			retyped = append(retyped, path+" ("+kind+" -> "+now+")")
		}
	}
	for path := range cur {
		if _, ok := old[path]; !ok {
			added = append(added, path)
		}
	}
	sort.Strings(removed)
	sort.Strings(retyped)
	sort.Strings(added)
	return removed, retyped, added
}

func formatShapeList(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return strings.Join(items, ", ")
}

func diffLines(want, got string) string {
	oldLines, newLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	first := 0
	for first < len(oldLines) && first < len(newLines) && oldLines[first] == newLines[first] {
		first++
	}
	oldEnd, newEnd := len(oldLines), len(newLines)
	for oldEnd > first && newEnd > first && oldLines[oldEnd-1] == newLines[newEnd-1] {
		oldEnd--
		newEnd--
	}
	var b strings.Builder
	for _, line := range oldLines[first:oldEnd] {
		b.WriteString("-" + line + "\n")
	}
	for _, line := range newLines[first:newEnd] {
		b.WriteString("+" + line + "\n")
	}
	return b.String()
}
