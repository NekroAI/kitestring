package store

import (
	"reflect"
	"testing"
)

func TestReqCodesIn(t *testing.T) {
	got := ReqCodesIn("feat: x（R-12）、r-3 与 R-12 / HR-4 / R-5a R-007")
	if want := []string{"R-12", "R-3", "R-7"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%v != %v", got, want)
	}
	if NormSHA("ABC1234") != "abc1234" || NormSHA("abc") != "" || NormSHA("xyz1234") != "" {
		t.Fatal("NormSHA")
	}
}
