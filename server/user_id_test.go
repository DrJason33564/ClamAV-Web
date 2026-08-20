package main

import "testing"

const (
	testAliceUserID = "alice001"
	testBobUserID   = "bob00002"
	testAdminUserID = "admin001"
)

func TestNewUserIDFormat(t *testing.T) {
	for i := 0; i < 100; i++ {
		id, err := newUserID()
		if err != nil {
			t.Fatal(err)
		}
		if !validUserID(id) {
			t.Fatalf("generated invalid user id %q", id)
		}
	}
}
