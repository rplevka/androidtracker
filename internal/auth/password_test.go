package auth

import "testing"

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("correct password was rejected")
	}
	if VerifyPassword(hash, "wrong password") {
		t.Fatal("wrong password was accepted")
	}
}

func TestPasswordHashesUseUniqueSalts(t *testing.T) {
	first, err := HashPassword("same password")
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword("same password")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("password hashes reused a salt")
	}
}

func TestMalformedPasswordHashesAreRejected(t *testing.T) {
	for _, encoded := range []string{
		"",
		"plaintext",
		"pbkdf2-sha256$1$abc$def",
		"bcrypt$600000$c2FsdA$aGFzaA",
		"pbkdf2-sha256$not-a-number$c2FsdA$aGFzaA",
	} {
		if VerifyPassword(encoded, "password") {
			t.Fatalf("malformed hash %q was accepted", encoded)
		}
	}
}
