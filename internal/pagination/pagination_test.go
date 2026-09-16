package pagination

import "testing"

func TestNormalize(t *testing.T) {
	t.Parallel()
	got, err := Normalize(Request{})
	if err != nil || got.Page != 1 || got.PageSize != 20 {
		t.Fatalf("Normalize() = %+v, %v", got, err)
	}
	if _, err := Normalize(Request{Page: 1, PageSize: MaxPageSize + 1}); err == nil {
		t.Fatal("oversized page passed")
	}
	if _, err := Normalize(Request{Page: MaxPage + 1, PageSize: 1}); err == nil {
		t.Fatal("oversized page number passed")
	}
}
