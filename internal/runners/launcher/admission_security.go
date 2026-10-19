package launcher

import (
	"reflect"
	"strconv"
	"strings"
)

// exactly renders CEL that admits the object at path only when it equals
// want field for field: every field of its type that want leaves unset,
// including seLinuxOptions and any later addition the API defines, must be
// absent. Each shape's security context is therefore pinned in full.
func exactly(path string, want any) string {
	v := reflect.ValueOf(want)
	if v.IsNil() {
		return "!has(" + path + ")"
	}
	return "has(" + path + ") && " + valueRule(path, v)
}

// safety: a kind this cannot render yields false, so admission fails closed.
func valueRule(path string, v reflect.Value) string {
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Struct:
		var rules []string
		for i := range v.NumField() {
			name, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("json"), ",")
			field, member := v.Field(i), path+"."+name
			if field.IsZero() && (field.Kind() == reflect.Pointer || field.Kind() == reflect.Slice || field.Kind() == reflect.String) {
				rules = append(rules, "!has("+member+")")
				continue
			}
			rules = append(rules, "has("+member+") && "+valueRule(member, field))
		}
		return "(" + strings.Join(rules, " && ") + ")"
	case reflect.Slice:
		items := make([]string, v.Len())
		for i := range v.Len() {
			items[i] = strconv.Quote(v.Index(i).String())
		}
		return path + " == [" + strings.Join(items, ", ") + "]"
	case reflect.String:
		return path + " == " + strconv.Quote(v.String())
	case reflect.Bool:
		return path + " == " + strconv.FormatBool(v.Bool())
	case reflect.Int64:
		return path + " == " + strconv.FormatInt(v.Int(), 10)
	}
	return "false"
}

// securityShapes pins the Pod and container security contexts of each shape.
func securityShapes() string {
	job := exactly("variables.spec.securityContext", jobPodSecurity()) + " && variables.containers.all(c, c.name == 'docker' ? " + exactly("c.securityContext", dockerSecurity()) + " : " + exactly("c.securityContext", workloadSecurity()) + ")"
	capacity := exactly("variables.spec.securityContext", probePodSecurity()) + " && variables.containers.all(c, " + exactly("c.securityContext", capacitySecurity()) + ")"
	probe := exactly("variables.spec.securityContext", probePodSecurity()) + " && variables.containers.all(c, " + exactly("c.securityContext", probeSecurity()) + ")"
	return "variables.shape == 'job' ? " + job + " : variables.shape == 'capacity' ? " + capacity + " : " + probe
}
