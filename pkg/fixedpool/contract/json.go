/*
 *     Copyright 2026 The Dragonfly Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

func Digest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func ValidDigest(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil && len(value) == 64 && value == strings.ToLower(value)
}

// Encode uses struct field order, compact UTF-8 JSON and exactly one final LF.
func Encode(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Decode rejects ambiguity before typed decoding: duplicate keys, nulls, missing
// fields (including zero-valued required fields), unknown fields and extra values.
func Decode(data []byte, value any) error { return decode(data, value, false) }

// DecodeRecord permits explicit unknown pointer values in preparation records.
func DecodeRecord(data []byte, value any) error { return decode(data, value, true) }

func decode(data []byte, value any, record bool) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := checkValue(decoder, record, "$"); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("expected end of JSON, got %v", err)
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	return requiredFields(data, reflect.TypeOf(value).Elem(), "$", record)
}

func checkValue(decoder *json.Decoder, nullable bool, location string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil && !nullable {
		return fmt.Errorf("%s: null is not allowed", location)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("%s: duplicate or invalid JSON key %q", location, key)
			}
			seen[name] = true
			if err := checkValue(decoder, nullable, location+"."+name); err != nil {
				return err
			}
		}
	case '[':
		for index := 0; decoder.More(); index++ {
			if err := checkValue(decoder, nullable, fmt.Sprintf("%s[%d]", location, index)); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	_, err = decoder.Token()
	return err
}

func requiredFields(data []byte, typ reflect.Type, location string, record bool) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		if record && (typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Interface) {
			return nil
		}
		return fmt.Errorf("%s: null is not allowed", location)
	}
	if typ.Kind() == reflect.Pointer {
		return requiredFields(data, typ.Elem(), location, record)
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")
			name := tag[0]
			if name == "-" || !field.IsExported() {
				continue
			}
			if name == "" {
				name = field.Name
			}
			raw, ok := fields[name]
			if !ok {
				if len(tag) > 1 && tag[1] == "omitempty" {
					continue
				}
				return fmt.Errorf("%s.%s: required field missing", location, name)
			}
			if err := requiredFields(raw, field.Type, location+"."+name, record); err != nil {
				return err
			}
			delete(fields, name)
		}
		// encoding/json folds field names even with DisallowUnknownFields.
		for name := range fields {
			return fmt.Errorf("%s: unknown or case-aliased JSON field %q", location, name)
		}
	case reflect.Slice, reflect.Array:
		var entries []json.RawMessage
		if err := json.Unmarshal(data, &entries); err != nil {
			return err
		}
		for i, entry := range entries {
			if err := requiredFields(entry, typ.Elem(), fmt.Sprintf("%s[%d]", location, i), record); err != nil {
				return err
			}
		}
	}
	return nil
}
