package auth

import (
	"errors"
	"strings"
	"testing"
)

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
	} else if !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("weak password error = %v", err)
	}
	if _, err := h.Verify("anything", "broken"); err == nil {
		t.Fatal("malformed hash accepted")
	}
	malformedParameters := "$argon2id$v=19$m=0,t=0,p=0$MDEyMzQ1Njc4OWFiY2RlZg$MDEyMzQ1Njc4OWFiY2RlZg"
	if ok, err := h.Verify("correct horse battery staple", malformedParameters); err == nil || ok {
		t.Fatal("zero-cost hash accepted")
	}
	if _, err := h.Hash(strings.Repeat("x", maxPasswordBytes+1)); err == nil {
		t.Fatal("oversized password accepted")
	}
}
