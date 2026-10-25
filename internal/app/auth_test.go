package app

import (
	"strings"
	"testing"
)

func TestPasswordPolicySaltAndVerificationBounds(t *testing.T) {
	policy := PasswordPolicy{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1, MaxMemoryKiB: 65536}
	password := "a-long-unique-fixture-password"
	first, e := HashPassword(password, policy)
	if e != nil {
		t.Fatal(e)
	}
	second, e := HashPassword(password, policy)
	if e != nil {
		t.Fatal(e)
	}
	if first == second {
		t.Fatal("password hashes reused a salt")
	}
	if !checkPassword(first, password, policy) || checkPassword(first, "wrong password", policy) {
		t.Fatal("Argon2id password verification failed")
	}
	hostile := strings.Replace(first, "m=19456", "m=4294967295", 1)
	if checkPassword(hostile, password, policy) {
		t.Fatal("unbounded KDF parameters accepted")
	}
	if _, e = HashPassword("short", policy); e == nil {
		t.Fatal("weak password accepted")
	}
	weak := policy
	weak.MemoryKiB = 8
	if _, e = HashPassword(password, weak); e == nil {
		t.Fatal("unsafe Argon2 resource policy accepted")
	}
}
