package app

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// ByteSize is a byte count that also accepts human-readable config strings.
type ByteSize int64

var byteSizePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([a-zA-Z]*)$`)

func (b *ByteSize) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	multiplier := int64(1)
	if strings.HasPrefix(text, `"`) {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		parts := byteSizePattern.FindStringSubmatch(strings.TrimSpace(value))
		if parts == nil {
			return fmt.Errorf("invalid byte size %q: expected a non-negative number and optional unit", value)
		}
		text = parts[1]
		switch strings.ToUpper(parts[2]) {
		case "", "B":
		case "K", "KIB":
			multiplier = 1 << 10
		case "M", "MIB":
			multiplier = 1 << 20
		case "G", "GIB":
			multiplier = 1 << 30
		case "T", "TIB":
			multiplier = 1 << 40
		case "KB":
			multiplier = 1000
		case "MB":
			multiplier = 1000 * 1000
		case "GB":
			multiplier = 1000 * 1000 * 1000
		case "TB":
			multiplier = 1000 * 1000 * 1000 * 1000
		default:
			return fmt.Errorf("invalid byte size: unknown unit %q", parts[2])
		}
	} else if !json.Valid(data) || len(text) == 0 || (text[0] != '-' && (text[0] < '0' || text[0] > '9')) {
		return fmt.Errorf("invalid byte size: expected a number or string")
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok {
		return fmt.Errorf("invalid byte size: invalid number %q", text)
	}
	if value.Sign() < 0 || strings.HasPrefix(text, "-") {
		return fmt.Errorf("invalid byte size: must be non-negative")
	}
	value.Mul(value, new(big.Rat).SetInt64(multiplier))
	if !value.IsInt() {
		return fmt.Errorf("invalid byte size: must resolve to whole bytes")
	}
	if !value.Num().IsInt64() {
		return fmt.Errorf("invalid byte size: exceeds int64 maximum")
	}
	*b = ByteSize(value.Num().Int64())
	return nil
}

func (b ByteSize) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatInt(int64(b), 10)), nil
}
