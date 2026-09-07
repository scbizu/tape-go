package entry

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestSeqIsComparableAndUnbounded(t *testing.T) {
	start := MustParseSeq("9999999999999999999999999999999999999999")
	want := MustParseSeq("10000000000000000000000000000000000000000")
	if got := start.Next(); got != want {
		t.Fatalf("Next() = %s, want %s", got, want)
	}
	values := map[Seq]string{want: "works as a map key"}
	if values[want] == "" {
		t.Fatal("Seq is not value-comparable")
	}
}

func TestSeqJSONUsesDecimalStringsOnly(t *testing.T) {
	huge := MustParseSeq("9007199254740993")
	data, err := json.Marshal(huge)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `"9007199254740993"` {
		t.Fatalf("MarshalJSON() = %s", data)
	}

	for _, data := range []string{`"0"`, `"18446744073709551616"`} {
		var got Seq
		if err := json.Unmarshal([]byte(data), &got); err != nil {
			t.Fatalf("UnmarshalJSON(%s): %v", data, err)
		}
	}

	for _, data := range []string{`0`, `1`, `18446744073709551615`, `null`} {
		var got Seq
		if err := json.Unmarshal([]byte(data), &got); !errors.Is(err, ErrInvalidSeq) {
			t.Errorf("UnmarshalJSON(%s) error = %v, want ErrInvalidSeq", data, err)
		}
	}
	if value, ok := SeqFromUint64(math.MaxUint64).Uint64(); !ok || value != math.MaxUint64 {
		t.Fatalf("Uint64() = %d, %v", value, ok)
	}
}

func TestParseSeqRejectsNonCanonicalValues(t *testing.T) {
	for _, value := range []string{"", "00", "01", "+1", "-1", "1.0", " 1"} {
		if _, err := ParseSeq(value); !errors.Is(err, ErrInvalidSeq) {
			t.Errorf("ParseSeq(%q) error = %v, want ErrInvalidSeq", value, err)
		}
	}
	if got, err := ParseSeq("0"); err != nil || !got.IsZero() {
		t.Fatalf("ParseSeq(0) = %s, %v", got, err)
	}
}
