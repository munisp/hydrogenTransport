package auth

import "testing"

func TestMarshalCheckRequestWireFormat(t *testing.T) {
	b := marshalCheckRequest("t1", "module", "telematics", "manage", "user-123")
	// Hand-verify structure: tenant(1), metadata(2), entity(3), permission(4), subject(5).
	expectContains := [][]byte{
		[]byte("t1"), []byte("module"), []byte("telematics"),
		[]byte("manage"), []byte("user"), []byte("user-123"),
	}
	for _, want := range expectContains {
		if !containsBytes(b, want) {
			t.Errorf("encoded CheckRequest missing %q\nbytes: %x", want, b)
		}
	}
	// depth=20 varint in metadata field 3 (key 0x18, value 0x14).
	if !containsBytes(b, []byte{0x18, 0x14}) {
		t.Errorf("encoded CheckRequest missing depth=20: %x", b)
	}
}

func TestParseCheckResponse(t *testing.T) {
	// can=1 (ALLOWED): field 1, varint 1.
	allowed := []byte{0x08, 0x01}
	if can, err := parseCheckResponse(allowed); err != nil || can != 1 {
		t.Errorf("allowed: can=%d err=%v", can, err)
	}
	// can=2 (DENIED) + metadata field 2 (len-delimited, empty).
	denied := []byte{0x08, 0x02, 0x12, 0x00}
	if can, err := parseCheckResponse(denied); err != nil || can != 2 {
		t.Errorf("denied: can=%d err=%v", can, err)
	}
	if _, err := parseCheckResponse([]byte{0x12, 0x00}); err == nil {
		t.Error("expected error for missing can field")
	}
	if _, err := parseCheckResponse([]byte{0x08}); err == nil {
		t.Error("expected error for truncated varint")
	}
}

func TestAudienceContains(t *testing.T) {
	cases := []struct {
		aud  any
		want bool
	}{
		{"h2fleet", true},
		{[]any{"other", "h2fleet"}, true},
		{[]string{"other"}, false},
		{nil, false},
		{"other", false},
	}
	for _, c := range cases {
		if got := audienceContains(c.aud, "h2fleet"); got != c.want {
			t.Errorf("audienceContains(%v) = %v, want %v", c.aud, got, c.want)
		}
	}
}

func containsBytes(haystack, needle []byte) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
