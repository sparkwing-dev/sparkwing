package sdkserve

import (
	"encoding/json"
	"reflect"
)

// EncodeServiceRequest encodes the body sent to a node-host service.
func EncodeServiceRequest(body any) ([]byte, error) {
	if body == nil {
		return json.Marshal(nil)
	}
	return json.Marshal(serviceCollections(reflect.ValueOf(body)).Interface())
}

func serviceCollections(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return value
		}
		if value.Kind() == reflect.Pointer {
			out := reflect.New(value.Type().Elem())
			out.Elem().Set(serviceCollections(value.Elem()))
			return out
		}
		out := reflect.New(value.Type()).Elem()
		out.Set(serviceCollections(value.Elem()))
		return out
	case reflect.Struct:
		out := reflect.New(value.Type()).Elem()
		out.Set(value)
		for i := range value.NumField() {
			if out.Field(i).CanSet() {
				out.Field(i).Set(serviceCollections(value.Field(i)))
			}
		}
		return out
	case reflect.Map:
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		for iter := value.MapRange(); iter.Next(); {
			out.SetMapIndex(iter.Key(), serviceCollections(iter.Value()))
		}
		return out
	case reflect.Slice, reflect.Array:
		out := reflect.New(value.Type()).Elem()
		if value.Kind() == reflect.Slice {
			out = reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		}
		for i := range value.Len() {
			out.Index(i).Set(serviceCollections(value.Index(i)))
		}
		return out
	default:
		return value
	}
}
