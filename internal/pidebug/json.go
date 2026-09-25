package pidebug

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

// Parse the exact tree before typed decoding. A RawMessage must not preserve
// members hidden by map last-key-wins behavior. Tokenization also recognizes
// duplicate names spelled with different JSON escapes.
func jsonTree(d *json.Decoder, depth int) (any, error) {
	if depth > 12 {
		return nil, ErrInvalid
	}
	token, err := d.Token()
	if err != nil {
		return nil, ErrInvalid
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return token, nil
	}
	switch delim {
	case '{':
		object := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, ErrInvalid
			}
			name, ok := key.(string)
			if !ok {
				return nil, ErrInvalid
			}
			if _, exists := object[name]; exists {
				return nil, ErrInvalid
			}
			value, err := jsonTree(d, depth+1)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		if end, err := d.Token(); err != nil || end != json.Delim('}') {
			return nil, ErrInvalid
		}
		return object, nil
	case '[':
		var array []any
		for d.More() {
			value, err := jsonTree(d, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if end, err := d.Token(); err != nil || end != json.Delim(']') {
			return nil, ErrInvalid
		}
		return array, nil
	default:
		return nil, ErrInvalid
	}
}

var rawMessageType = reflect.TypeFor[json.RawMessage]()

// JSON tags are the sole field list: no parallel required-key table to drift.
// Required pointers (runtime/usage) allow explicit null for unknown results;
// omitted optional pointers do not authorize a present-but-null object.
func requiredShape(value any, typ reflect.Type, nullable bool) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == rawMessageType || typ.Kind() == reflect.Interface {
		return nil
	}
	if value == nil {
		if nullable {
			return nil
		}
		return ErrInvalid
	}
	switch typ.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		fields := map[string]reflect.StructField{}
		var collect func(reflect.Type)
		collect = func(t reflect.Type) {
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				if !f.IsExported() {
					continue
				}
				tag := f.Tag.Get("json")
				if f.Anonymous && tag == "" {
					collect(f.Type)
					continue
				}
				name := strings.Split(tag, ",")[0]
				if name == "-" {
					continue
				}
				if name == "" {
					name = f.Name
				}
				fields[name] = f
			}
		}
		collect(typ)
		for key := range object {
			if _, ok := fields[key]; !ok {
				return ErrInvalid
			}
		}
		for key, field := range fields {
			optional := strings.Contains(field.Tag.Get("json"), ",omitempty")
			v, present := object[key]
			if !present {
				if optional {
					continue
				}
				return ErrInvalid
			}
			if requiredShape(v, field.Type, field.Type.Kind() == reflect.Pointer && !optional) != nil {
				return ErrInvalid
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		for _, v := range object {
			if requiredShape(v, typ.Elem(), false) != nil {
				return ErrInvalid
			}
		}
	case reflect.Slice, reflect.Array:
		array, ok := value.([]any)
		if !ok {
			return ErrInvalid
		}
		for _, v := range array {
			if requiredShape(v, typ.Elem(), false) != nil {
				return ErrInvalid
			}
		}
	}
	return nil
}

// Decode enforces encoded size, UTF-8, depth, unique members, exact required
// presence and closed field names. Failures never quote captured values.
func Decode(raw []byte, target any) error {
	if len(raw) > MaxEventBytes || !utf8.Valid(raw) || target == nil {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	tree, err := jsonTree(d, 0)
	if err != nil {
		return ErrInvalid
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	if requiredShape(tree, reflect.TypeOf(target), false) != nil {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if d.Decode(target) != nil || !errors.Is(d.Decode(new(any)), io.EOF) {
		return ErrInvalid
	}
	return nil
}
