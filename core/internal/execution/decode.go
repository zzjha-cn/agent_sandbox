package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxTaskBytes = 1 << 20

func DecodeTaskSpec(r io.Reader) (TaskSpec, error) {
	var spec TaskSpec
	b, err := io.ReadAll(io.LimitReader(r, MaxTaskBytes+1))
	if err != nil {
		return spec, err
	}
	if len(b) > MaxTaskBytes {
		return spec, errors.New("task input exceeds 1 MiB")
	}
	if !utf8.Valid(b) {
		return spec, errors.New("task input must be UTF-8")
	}
	normalized, err := normalizeTaskJSON(b, reflect.TypeOf(spec))
	if err != nil {
		return spec, err
	}
	decoder := json.NewDecoder(bytes.NewReader(normalized))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return spec, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return spec, errors.New("task input must contain exactly one JSON object")
	}
	if err := spec.ValidateInput(); err != nil {
		return spec, err
	}
	return spec, nil
}

// Normalize against the exact JSON field names before Go's case-insensitive
// decoder runs. Reject duplicates and null at every depth, including call values.
func normalizeTaskJSON(raw []byte, typ reflect.Type) ([]byte, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("task fields must not be null; omit optional fields")
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct, reflect.Map:
		dec := json.NewDecoder(bytes.NewReader(raw))
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if tok != json.Delim('{') {
			return nil, errors.New("expected object")
		}
		fields := map[string]reflect.Type{}
		if typ.Kind() == reflect.Struct {
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				name := strings.Split(f.Tag.Get("json"), ",")[0]
				if name != "" && name != "-" {
					fields[name] = f.Type
				}
			}
		}
		result := map[string]json.RawMessage{}
		for dec.More() {
			tok, err = dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := tok.(string)
			if !ok {
				return nil, errors.New("expected object key")
			}
			if _, ok := result[key]; ok {
				return nil, fmt.Errorf("duplicate task field %q", key)
			}
			ft, ok := fields[key]
			if typ.Kind() == reflect.Map {
				ft, ok = typ.Elem(), true
			}
			if !ok {
				return nil, fmt.Errorf("unknown task field %q", key)
			}
			var value json.RawMessage
			if err = dec.Decode(&value); err != nil {
				return nil, err
			}
			value, err = normalizeTaskJSON(value, ft)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			result[key] = value
		}
		if _, err = dec.Token(); err != nil {
			return nil, err
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			return nil, errors.New("trailing task data")
		}
		return json.Marshal(result)
	case reflect.Slice:
		var elements []json.RawMessage
		if err := json.Unmarshal(raw, &elements); err != nil {
			return nil, err
		}
		for i, v := range elements {
			n, err := normalizeTaskJSON(v, typ.Elem())
			if err != nil {
				return nil, err
			}
			elements[i] = n
		}
		return json.Marshal(elements)
	case reflect.Int:
		n, err := taskInteger(json.RawMessage(raw))
		if err != nil {
			return nil, err
		}
		return []byte(strconv.Itoa(n)), nil
	default:
		return raw, nil
	}
}

func taskInteger(raw json.RawMessage) (int, error) {
	number := strings.TrimSpace(string(raw))
	if len(number) == 0 || (number[0] != '-' && (number[0] < '0' || number[0] > '9')) {
		return 0, fmt.Errorf("must be an integer")
	}
	negative := strings.HasPrefix(number, "-")
	number = strings.TrimPrefix(number, "-")
	coefficient, exponent := number, "0"
	if i := strings.IndexAny(number, "eE"); i >= 0 {
		coefficient, exponent = number[:i], number[i+1:]
	}
	fraction := 0
	if i := strings.IndexByte(coefficient, '.'); i >= 0 {
		fraction = len(coefficient) - i - 1
		coefficient = coefficient[:i] + coefficient[i+1:]
	}
	digits := strings.TrimLeft(coefficient, "0")
	if digits == "" {
		return 0, nil
	}
	exp, err := strconv.ParseInt(exponent, 10, 64)
	if err != nil || exp > int64(len(raw))+6 || exp < -int64(len(raw))-6 {
		return 0, fmt.Errorf("must be an integer between 0 and %d", 86400)
	}
	scale := exp - int64(fraction)
	if scale < 0 {
		cut := -scale
		if cut > int64(len(digits)) || strings.Trim(digits[int64(len(digits))-cut:], "0") != "" {
			return 0, fmt.Errorf("must be an integer")
		}
		digits = digits[:int64(len(digits))-cut]
	} else {
		if int64(len(digits))+scale > int64(len(strconv.Itoa(86400))) {
			return 0, fmt.Errorf("exceeds maximum timeout")
		}
		digits += strings.Repeat("0", int(scale))
	}
	if negative || len(digits) > len(strconv.Itoa(86400)) {
		return 0, fmt.Errorf("must be between 0 and %d", 86400)
	}
	value, err := strconv.Atoi(digits)
	if err != nil || value > 86400 {
		return 0, fmt.Errorf("must be between 0 and %d", 86400)
	}
	return value, nil
}
