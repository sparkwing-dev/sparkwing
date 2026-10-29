package launcher

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// exactly renders CEL that admits the object at path only when it equals
// want field for field: every field of its type that want leaves unset,
// including seLinuxOptions, must be absent. The fields are those of the
// vendored k8s.io/api, so a field a newer API server adds is unpinned until
// that dependency moves. The launcher builds every spec these rules judge:
// capacity Pods directly, Job Pods through the Job controller from its templates.
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
	capacity := exactly("variables.spec.securityContext", capacityPodSecurity()) + " && variables.containers.all(c, " + exactly("c.securityContext", capacitySecurity()) + ")"
	return "variables.shape == 'job' ? " + job + " : " + capacity
}

// hostCommand pins a workload container's command and argument grammar. The
// award's controller, run, generation and job values vary; every flag name and
// path is fixed.
func hostCommand(c, verb string) string {
	award := CloudJobAward{Binding: CloudJobBinding{RunID: "\x00run", NodeID: "\x00job", Generation: 1}}
	sample := hostContainer(CloudJobConfig{ControllerURL: "\x00controller"}, award, verb)
	args := sample.Args
	rules := []string{c + ".?command.orValue([]) == " + celList(sample.Command), "!has(" + c + ".restartPolicy)"}
	sizes := []string{"size(" + c + ".?args.orValue([])) == " + strconv.Itoa(len(args)-2)}
	jobIndex := slices.Index(args, "--job")
	sizes = append(sizes, "size("+c+".?args.orValue([])) == "+strconv.Itoa(len(args))+" && "+c+".args["+strconv.Itoa(jobIndex)+"] == '--job'")
	rules = append(rules, "("+strings.Join(sizes, " || ")+")")
	for i, arg := range args[:jobIndex] {
		if strings.HasPrefix(arg, "\x00") || arg == "1" && args[i-1] == "--generation" {
			continue
		}
		rules = append(rules, c+".args["+strconv.Itoa(i)+"] == "+strconv.Quote(arg))
	}
	return strings.Join(rules, " && ")
}
