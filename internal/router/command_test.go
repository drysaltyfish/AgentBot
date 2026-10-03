package router

import (
	"errors"
	"reflect"
	"testing"
)

func Test_F81_ParseCommandArgsTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{"plain", "a b c", []string{"a", "b", "c"}, false},
		{"double quotes", `he said "hello world"`, []string{"he", "said", "hello world"}, false},
		{"single quotes", `say 'hi there'`, []string{"say", "hi there"}, false},
		{"escaped space", `a\ b`, []string{"a b"}, false},
		{"collapsed whitespace", "   a    b   ", []string{"a", "b"}, false},
		{"unterminated quote", `say "hi`, nil, true},
	}
	for _, tc := range cases {
		got, err := ParseCommandArgs(tc.in)
		if tc.wantErr {
			if !errors.Is(err, ErrUnterminatedQuote) {
				t.Fatalf("%s: actual=%v expected=ErrUnterminatedQuote", tc.name, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: actual=%v expected=nil", tc.name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: actual=%q (len=%d) expected=%q (len=%d)", tc.name, got, len(got), tc.want, len(tc.want))
		}
	}
}

func Test_F81_EmptyInputReturnsEmptySliceNotNil(t *testing.T) {
	t.Parallel()
	got, err := ParseCommandArgs("")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got == nil {
		t.Fatalf("empty input: actual=nil expected=empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("empty input: actual=%v expected=[]", got)
	}
}
