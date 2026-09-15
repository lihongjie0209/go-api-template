package auth

import "testing"

func TestPasswordHasher(t *testing.T) {
	t.Parallel()
	h := NewPasswordHasher()
	encoded, err := h.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := h.Verify("correct horse battery staple", encoded)
	if err != nil || !ok {
		t.Fatalf("Verify()=%v,%v", ok, err)
	}
	ok, err = h.Verify("wrong password", encoded)
	if err != nil || ok {
		t.Fatalf("wrong Verify()=%v,%v", ok, err)
	}
}
func TestPasswordHasherRejectsWeakAndMalformed(t *testing.T) {
	t.Parallel()
	h := NewPasswordHasher()
	if _, err := h.Hash("short"); err == nil {
		t.Fatal("weak password accepted")
	}
	if _, err := h.Verify("anything", "broken"); err == nil {
		t.Fatal("malformed hash accepted")
	}
}
