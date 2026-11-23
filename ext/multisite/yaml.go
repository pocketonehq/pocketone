package multisite

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// DecodeExtensionConfig fills out from an instance's extension settings: a value of out's own type (or a pointer to it,
// as a host application's instance source would set) is copied; anything else (e.g. a map from YAML, or from JSON when
// the instance crossed the wire) is converted through YAML. A key out has no field for is an error, so settings written
// under the wrong names fail loudly instead of decoding to nothing. A nil value leaves out unchanged.
func DecodeExtensionConfig[T any](v any, out *T) error {
	switch x := v.(type) {
	case nil:
		return nil
	case T:
		*out = x
		return nil
	case *T:
		if x != nil {
			*out = *x
		}
		return nil
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("extension settings: %w", err)
	}
	return nil
}
