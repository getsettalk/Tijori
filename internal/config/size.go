package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Size is a byte count that can be written in config as a number of bytes or
// as a human string such as "500MB" or "10GB". Units are powers of 1024.
// Zero means "no limit" wherever a Size is used as a limit.
type Size int64

var sizeUnits = []struct {
	suffix string
	mult   int64
}{
	{"TIB", 1 << 40}, {"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10},
	{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10},
	{"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10},
	{"B", 1},
}

// ParseSize parses strings like "10GB", "1.5G", "500 MB", "1024".
func ParseSize(s string) (Size, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	if t == "" || t == "0" {
		return 0, nil
	}
	mult := int64(1)
	for _, u := range sizeUnits {
		if strings.HasSuffix(t, u.suffix) {
			mult = u.mult
			t = strings.TrimSpace(strings.TrimSuffix(t, u.suffix))
			break
		}
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid size %q (examples: 500MB, 10GB, 1TB)", s)
	}
	return Size(f * float64(mult)), nil
}

func (s Size) String() string { return FormatBytes(int64(s)) }

// UnmarshalText lets TOML strings like "10GB" decode into a Size.
func (s *Size) UnmarshalText(b []byte) error {
	v, err := ParseSize(string(b))
	if err != nil {
		return err
	}
	*s = v
	return nil
}

// UnmarshalTOML accepts both integers and strings.
func (s *Size) UnmarshalTOML(v any) error {
	switch x := v.(type) {
	case int64:
		*s = Size(x)
		return nil
	case float64:
		*s = Size(x)
		return nil
	case string:
		return s.UnmarshalText([]byte(x))
	}
	return fmt.Errorf("invalid size value %v", v)
}

func (s Size) MarshalText() ([]byte, error) {
	if s == 0 {
		return []byte(""), nil
	}
	return []byte(FormatBytes(int64(s))), nil
}

// FormatBytes renders a byte count for humans, for example "1.5 GB".
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	s := strconv.FormatFloat(v, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s + " " + string("KMGTP"[exp]) + "B"
}
