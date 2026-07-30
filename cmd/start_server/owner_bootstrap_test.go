package main

import (
	"reflect"
	"testing"
)

func TestMissingOwnerEnvVars(t *testing.T) {
	tests := []struct {
		name     string
		username string
		email    string
		password string
		want     []string
	}{
		{name: "none", want: []string{"OWNER_USERNAME", "OWNER_EMAIL", "OWNER_PASSWORD"}},
		{name: "password missing", username: "admin", email: "admin@example.com", want: []string{"OWNER_PASSWORD"}},
		{name: "username email missing", password: "secret", want: []string{"OWNER_USERNAME", "OWNER_EMAIL"}},
		{name: "complete", username: "admin", email: "admin@example.com", password: "secret", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := missingOwnerEnvVars(tt.username, tt.email, tt.password)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("missingOwnerEnvVars() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
