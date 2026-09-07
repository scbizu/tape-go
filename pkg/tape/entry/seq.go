package entry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var ErrInvalidSeq = errors.New("entry: invalid sequence")

// Seq is an immutable, arbitrary-precision entry sequence.
//
// Its zero value is the unassigned sequence. Positive values are stored as a
// private canonical decimal string, which keeps Seq value-comparable without
// exposing mutable big-integer state.
type Seq struct {
	decimal string
}

// SeqFromUint64 converts a legacy-width value to Seq.
func SeqFromUint64(value uint64) Seq {
	if value == 0 {
		return Seq{}
	}
	return Seq{decimal: strconv.FormatUint(value, 10)}
}

// ParseSeq parses a canonical unsigned decimal sequence.
func ParseSeq(value string) (Seq, error) {
	if value == "0" {
		return Seq{}, nil
	}
	if value == "" || value[0] == '0' {
		return Seq{}, fmt.Errorf("%w: %q is not canonical", ErrInvalidSeq, value)
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return Seq{}, fmt.Errorf("%w: %q is not an unsigned decimal", ErrInvalidSeq, value)
		}
	}
	return Seq{decimal: value}, nil
}

// MustParseSeq is ParseSeq for constants and test fixtures.
func MustParseSeq(value string) Seq {
	seq, err := ParseSeq(value)
	if err != nil {
		panic(err)
	}
	return seq
}

// IsZero reports whether s is the unassigned sequence.
func (s Seq) IsZero() bool {
	return s.decimal == ""
}

// String returns the canonical unsigned decimal representation of s.
func (s Seq) String() string {
	if s.IsZero() {
		return "0"
	}
	return s.decimal
}

// Cmp compares s and other numerically.
func (s Seq) Cmp(other Seq) int {
	left := s.String()
	right := other.String()
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return strings.Compare(left, right)
}

// Next returns the numeric successor of s.
func (s Seq) Next() Seq {
	digits := []byte(s.String())
	for i := len(digits) - 1; i >= 0; i-- {
		if digits[i] < '9' {
			digits[i]++
			return Seq{decimal: string(digits)}
		}
		digits[i] = '0'
	}
	return Seq{decimal: "1" + string(digits)}
}

// Uint64 returns s as uint64 when it fits.
func (s Seq) Uint64() (uint64, bool) {
	value, err := strconv.ParseUint(s.String(), 10, 64)
	return value, err == nil
}

func (s Seq) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

func (s *Seq) UnmarshalJSON(data []byte) error {
	if s == nil {
		return errors.New("entry: unmarshal sequence into nil receiver")
	}
	var value string
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidSeq, err)
		}
	} else {
		// Bare JSON numbers are accepted only for legacy uint64 data. New data is
		// always emitted as a string so JavaScript consumers cannot lose precision.
		if len(data) == 0 || !bytes.Equal(bytes.TrimSpace(data), data) {
			return fmt.Errorf("%w: malformed JSON number", ErrInvalidSeq)
		}
		if _, err := strconv.ParseUint(string(data), 10, 64); err != nil {
			return fmt.Errorf("%w: legacy number %q: %v", ErrInvalidSeq, data, err)
		}
		value = string(data)
	}
	seq, err := ParseSeq(value)
	if err != nil {
		return err
	}
	*s = seq
	return nil
}

func (s Seq) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

func (s *Seq) UnmarshalText(text []byte) error {
	seq, err := ParseSeq(string(text))
	if err != nil {
		return err
	}
	*s = seq
	return nil
}
