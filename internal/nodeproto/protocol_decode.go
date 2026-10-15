package nodeproto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// safety: encoding/json accepts null scalars and duplicate keys; neither may silently change a wire value.
func checkProtocolValue(raw json.RawMessage, kind reflect.Type) error {
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	if kind == reflect.TypeFor[json.RawMessage]() || kind.Kind() == reflect.Interface {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("%w: null is not a typed wire value", ErrMalformed)
	}
	if kind == reflect.TypeFor[time.Time]() {
		return nil
	}
	switch kind.Kind() {
	case reflect.Struct, reflect.Map:
		obj, err := protocolObject(raw)
		if err != nil {
			return err
		}
		for key, value := range obj {
			var child reflect.Type
			if kind.Kind() == reflect.Map {
				child = kind.Elem()
			} else {
				var found bool
				child, found = planFieldType(kind, key)
				if !found {
					for i := range kind.NumField() {
						name, _, _ := strings.Cut(kind.Field(i).Tag.Get("json"), ",")
						if strings.EqualFold(name, key) {
							return fmt.Errorf("%w: wire fields require their exact spelling", ErrMalformed)
						}
					}
					continue
				}
			}
			if err := checkProtocolValue(value, child); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return fmt.Errorf("%w: expected an array", ErrMalformed)
		}
		for _, value := range values {
			if err := checkProtocolValue(value, kind.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
