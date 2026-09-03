package main

import (
	"context"
	"errors"
	"testing"

	"github.com/NishilRathod/gitscout/internal/github"
)

func TestResolveLogin(t *testing.T) {
	ok := func(login string) func(context.Context) (github.User, error) {
		return func(context.Context) (github.User, error) {
			return github.User{Login: login}, nil
		}
	}
	// The real 403 from GitHub Actions: the supplied token is an app
	// installation token and has no user attached to it.
	notAUser := func(context.Context) (github.User, error) {
		return github.User{}, errors.New("403: Resource not accessible by integration")
	}
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}

	tests := []struct {
		name     string
		flagUser string
		viewer   func(context.Context) (github.User, error)
		env      map[string]string
		want     string
		wantErr  bool
	}{
		{
			name:     "the flag wins outright",
			flagUser: "someoneelse",
			viewer:   ok("NishilRathod"),
			env:      map[string]string{"GITHUB_REPOSITORY_OWNER": "someorg"},
			want:     "someoneelse",
		},
		{
			name:   "a local run uses the token's owner",
			viewer: ok("NishilRathod"),
			want:   "NishilRathod",
		},
		{
			name:   "the token's owner beats the environment when both work",
			viewer: ok("NishilRathod"),
			env:    map[string]string{"GITHUB_REPOSITORY_OWNER": "someorg"},
			want:   "NishilRathod",
		},
		{
			name:   "in Actions, /user 403s and the repository owner is used",
			viewer: notAUser,
			env:    map[string]string{"GITHUB_REPOSITORY_OWNER": "NishilRathod"},
			want:   "NishilRathod",
		},
		{
			name:   "an empty login is not an identity",
			viewer: ok(""),
			env:    map[string]string{"GITHUB_REPOSITORY_OWNER": "NishilRathod"},
			want:   "NishilRathod",
		},
		{
			name:    "nothing to go on is an error, not a guess",
			viewer:  notAUser,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveLogin(context.Background(), tt.flagUser, tt.viewer, env(tt.env))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("got %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
