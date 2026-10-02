package router

import (
	"errors"
	"reflect"
	"testing"
	"time"
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

type pingArgs struct {
	Verbose bool          `flag:"v"`
	Count   int           `flag:"count,default=3"`
	Limit   int64         `flag:"limit,default=10"`
	Ratio   float64       `flag:"ratio,default=0.5"`
	Name    string        `flag:"name,default=anonymous"`
	Wait    time.Duration `flag:"wait,default=2s"`
	Ignored string
}

func Test_F81_BindFlagsSupportedTypes(t *testing.T) {
	t.Parallel()
	var args pingArgs
	err := BindFlags(&args, []string{"-v", "-count", "7", "-limit", "42", "-ratio", "1.5", "-name", "bob", "-wait", "5s"})
	if err != nil {
		t.Fatalf("BindFlags: actual=%v expected=nil", err)
	}
	if !args.Verbose || args.Count != 7 || args.Limit != 42 || args.Ratio != 1.5 || args.Name != "bob" || args.Wait != 5*time.Second {
		t.Fatalf("bound struct: actual=%+v", args)
	}

	var defaults pingArgs
	if err := BindFlags(&defaults, nil); err != nil {
		t.Fatalf("BindFlags with no args: %v", err)
	}
	if defaults.Count != 3 || defaults.Limit != 10 || defaults.Ratio != 0.5 || defaults.Name != "anonymous" || defaults.Wait != 2*time.Second {
		t.Fatalf("defaults: actual=%+v", defaults)
	}
	if defaults.Verbose {
		t.Fatalf("bool default should be false: actual=true")
	}
}

func Test_F81_UnknownFlagIsRejectedByDefault(t *testing.T) {
	t.Parallel()
	var args pingArgs
	if err := BindFlags(&args, []string{"-nope"}); err == nil {
		t.Fatalf("unknown flag: actual=nil expected=error")
	}
	if err := BindFlagsOpt(&args, []string{"-nope", "-count", "9"}, BindOptions{IgnoreUnknown: true}); err != nil {
		t.Fatalf("IgnoreUnknown: actual=%v expected=nil", err)
	}
	if args.Count != 9 {
		t.Fatalf("known flag after ignored unknown: actual=%d expected=9", args.Count)
	}
}

func Test_F81_UnsupportedFieldTypeIsRejected(t *testing.T) {
	t.Parallel()
	type badArgs struct {
		Chan chan int `flag:"c"`
	}
	var bad badArgs
	err := BindFlags(&bad, nil)
	if !errors.Is(err, ErrUnsupportedFlagType) {
		t.Fatalf("unsupported type: actual=%v expected=ErrUnsupportedFlagType", err)
	}
}

func Test_F81_BadReceiverIsRejected(t *testing.T) {
	t.Parallel()
	for _, v := range []any{42, pingArgs{}, (*pingArgs)(nil)} {
		if err := BindFlags(v, nil); !errors.Is(err, ErrBadReceiver) {
			t.Fatalf("BindFlags(%T): actual=%v expected=ErrBadReceiver", v, err)
		}
	}
}
