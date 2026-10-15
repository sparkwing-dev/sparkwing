package nodeproto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func planInvalid(path, reason string) error {
	return &apierr.Error{Code: apierr.PlanInvalid, Message: fmt.Sprintf("invalid plan at %s: %s", path, reason)}
}

func strictPlanDecode(raw json.RawMessage, secrets SecretMatcher) (PlanDoc, error) {
	var plan PlanDoc
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := checkPlanValue(decoder, reflect.TypeOf(plan), "plan", secrets); err != nil {
		return PlanDoc{}, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return PlanDoc{}, planInvalid("plan", "trailing JSON or malformed document")
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		return PlanDoc{}, planInvalid("plan", "field has an invalid type or number")
	}
	return plan, nil
}

func checkPlanValue(decoder *json.Decoder, kind reflect.Type, path string, secrets SecretMatcher) error {
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	token, err := decoder.Token()
	if err != nil {
		return planInvalid(path, "malformed JSON")
	}
	if token == nil {
		return planInvalid(path, "null is not a declaration")
	}
	open, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch open {
	case '{':
		if kind.Kind() != reflect.Struct && kind.Kind() != reflect.Map {
			return planInvalid(path, "unexpected object")
		}
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return planInvalid(path, "malformed object")
			}
			key, ok := keyToken.(string)
			if !ok {
				return planInvalid(path, "non-string key")
			}
			fieldName := key
			if secrets.ContainsSecret(key) {
				fieldName = "[secret]"
			}
			childPath := path + "." + fieldName
			if seen[key] {
				return planInvalid(childPath, "duplicate field")
			}
			seen[key] = true
			var childType reflect.Type
			if kind.Kind() == reflect.Map {
				childType = kind.Elem()
			} else {
				var found bool
				childType, found = planFieldType(kind, key)
				if !found {
					return &apierr.Error{Code: apierr.PlanInvalid, Message: fmt.Sprintf("invalid plan at %s: unknown field", childPath), Hint: fmt.Sprintf("Upgrade the SDK or remove %s", childPath)}
				}
			}
			if err := checkPlanValue(decoder, childType, childPath, secrets); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return planInvalid(path, "malformed object")
		}
		if kind.Kind() == reflect.Struct {
			for i := 0; i < kind.NumField(); i++ {
				field := kind.Field(i)
				tag := strings.Split(field.Tag.Get("json"), ",")
				if len(tag) == 1 && tag[0] != "" && tag[0] != "-" && !seen[tag[0]] {
					return planInvalid(path+"."+tag[0], "required field is absent")
				}
			}
		}
	case '[':
		if kind.Kind() != reflect.Slice && kind.Kind() != reflect.Array {
			return planInvalid(path, "unexpected array")
		}
		for i := 0; decoder.More(); i++ {
			if err := checkPlanValue(decoder, kind.Elem(), fmt.Sprintf("%s[%d]", path, i), secrets); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return planInvalid(path, "malformed array")
		}
	default:
		return planInvalid(path, "malformed JSON")
	}
	return nil
}

func planFieldType(kind reflect.Type, key string) (reflect.Type, bool) {
	for i := 0; i < kind.NumField(); i++ {
		field := kind.Field(i)
		if strings.Split(field.Tag.Get("json"), ",")[0] == key {
			return field.Type, true
		}
	}
	return nil, false
}
