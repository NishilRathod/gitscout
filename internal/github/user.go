package github

import (
	"context"
	"fmt"
	"net/url"
)

// User fetches an account's public profile.
func (c *Client) User(ctx context.Context, login string) (User, error) {
	var u User
	if _, err := c.get(ctx, "/users/"+login, &u); err != nil {
		return User{}, err
	}
	return u, nil
}

// UserRepos lists an account's public repositories, following pagination.
func (c *Client) UserRepos(ctx context.Context, login string) ([]Repo, error) {
	var out []Repo
	for page := 1; page <= 10; page++ {
		q := url.Values{}
		q.Set("per_page", "100")
		q.Set("page", fmt.Sprint(page))
		q.Set("sort", "updated")

		var batch []Repo
		if _, err := c.get(ctx, "/users/"+login+"/repos?"+q.Encode(), &batch); err != nil {
			return out, err
		}
		out = append(out, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return out, nil
}

// RepoLanguages returns the byte count per language for a repository. Byte
// counts matter more than the single "primary language" field: a repo GitHub
// labels TypeScript may be a third CSS, and weighting by volume gives a truer
// picture of what someone has actually written.
func (c *Client) RepoLanguages(ctx context.Context, fullName string) (map[string]int, error) {
	langs := map[string]int{}
	if _, err := c.get(ctx, "/repos/"+fullName+"/languages", &langs); err != nil {
		return nil, err
	}
	return langs, nil
}

// Viewer returns the account the token belongs to, so the tool can personalise
// itself without being told who is running it.
func (c *Client) Viewer(ctx context.Context) (User, error) {
	var u User
	if _, err := c.get(ctx, "/user", &u); err != nil {
		return User{}, err
	}
	return u, nil
}
