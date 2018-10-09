package tasks

import "testing"

var csvFile = []byte(`Name,Em,Address,Country,Phone Number,Github Username
Kevin Burke,kevin@burke.services,"123 Main St",USA,925-555-1234,kevinburke
Kevin Foo,anotheremail@burke.services,"123 Main St",USA,925-555-1234,kevinburke_test
TestUser,test@example.com,"123 Main St",USA,925-555-1234,test
`)

func TestGetUsernames(t *testing.T) {
	usernames, err := getUsernames(csvFile, "github username")
	if err != nil {
		t.Fatal(err)
	}
	if len(usernames) != 3 {
		t.Errorf("wrong number of emails: want 3 got %d", len(usernames))
	}
	if usernames[0] != "kevinburke" {
		t.Errorf("wrong value for first email: want kevinburke, got %q", usernames[0])
	}
}
