package cfg

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Bool is a bool that also decodes from "true" and "false", so a BCL value can
// come from an environment variable: env("ALLOW", "false").
type Bool bool

// UnmarshalJSON implements json.Unmarshaler.
func (b *Bool) UnmarshalJSON(raw []byte) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case bool:
		*b = Bool(x)
	case string:
		parsed, err := strconv.ParseBool(x)
		if err != nil {
			return fmt.Errorf("invalid boolean %q", x)
		}
		*b = Bool(parsed)
	case nil:
		*b = false
	default:
		return fmt.Errorf("invalid boolean %v", v)
	}
	return nil
}
