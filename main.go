/*
Copyright © 2023 The Kubernetes Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/go-github/v48/github"
	githubql "github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

const (
	// perPage is the number of items to return per page.
	perPage = 100
	// orgName is the name of the GitHub organization to query.
	orgName = "kubernetes"
	// projectName is the name of the GitHub project to query.
	projectName = "SIG Auth"
)

type ghClient struct {
	*github.Client
	v4Client *githubql.Client
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// GITHUB_TOKEN is a personal access token with the following scopes:
	// - repo (all)
	// - read:org
	// - project (all)
	token := os.Getenv("GITHUB_TOKEN")
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := ghClient{Client: github.NewClient(tc), v4Client: githubql.NewClient(tc)}

	projectID, err := client.getProjectID(ctx, orgName, projectName)
	must(err)

	repos, err := client.listRepos(ctx, orgName)
	must(err)

	for _, repo := range repos {
		fmt.Printf("Looking for issues and PRs in %s/%s\n", orgName, *repo.Name)

		items, err := client.listIssuesAndPullRequests(ctx, orgName, *repo.Name, "sig/auth")
		must(err)

		fmt.Printf("found %d in repo %s/%s\n", len(items), orgName, *repo.Name)
		for _, item := range items {
			fmt.Printf("adding [%d] %s to project\n", *item.Number, *item.Title)
			err := client.addProjectV2ItemById(ctx, projectID, *item.NodeID)
			must(err)
		}
	}
}

func (c *ghClient) listRepos(ctx context.Context, org string) ([]*github.Repository, error) {
	var allRepos []*github.Repository
	opt := &github.RepositoryListByOrgOptions{
		ListOptions: github.ListOptions{PerPage: perPage},
	}

	for {
		repos, resp, err := c.Repositories.ListByOrg(ctx, org, opt)
		if err != nil {
			return nil, err
		}
		allRepos = append(allRepos, repos...)
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return allRepos, nil
}

func (c *ghClient) listIssuesAndPullRequests(ctx context.Context, owner, repo string, labels ...string) ([]*github.Issue, error) {
	var allIssues []*github.Issue
	opts := &github.IssueListByRepoOptions{
		Labels: labels,
		ListOptions: github.ListOptions{
			PerPage: perPage,
		},
	}

	for {
		// Note: As far as the GitHub API is concerned, every pull request is an issue,
		// but not every issue is a pull request. Some endpoints, events, and webhooks
		// may also return pull requests via this struct. If PullRequestLinks is nil,
		// this is an issue, and if PullRequestLinks is not nil, this is a pull request.
		// The IsPullRequest helper method can be used to check that.
		// xref: https://docs.github.com/en/rest/issues/issues?apiVersion=2022-11-28#list-repository-issues
		issues, resp, err := c.Issues.ListByRepo(ctx, owner, repo, opts)
		if err != nil {
			return nil, err
		}
		allIssues = append(allIssues, issues...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return allIssues, nil
}

func (c *ghClient) getProjectID(ctx context.Context, org, name string) (githubql.ID, error) {
	var query struct {
		Organization struct {
			ProjectV2 struct {
				Nodes []struct {
					ID    githubql.ID     `graphql:"id"`
					Title githubql.String `graphql:"title"`
				} `graphql:"nodes"`
			} `graphql:"projectsV2(first: 100)"`
		} `graphql:"organization(login: $org)"`
	}

	variables := map[string]interface{}{
		"org": githubql.String(org),
	}

	err := c.v4Client.Query(ctx, &query, variables)
	if err != nil {
		return nil, err
	}

	for _, project := range query.Organization.ProjectV2.Nodes {
		if project.Title == githubql.String(name) {
			fmt.Printf("found project %q with ID %q\n", name, project.ID)
			return project.ID, nil
		}
	}

	return nil, fmt.Errorf("project %q not found", name)
}

func (c *ghClient) addProjectV2ItemById(ctx context.Context, projectID, contentID githubql.ID) error {
	// xref: https://docs.github.com/en/issues/planning-and-tracking-with-projects/automating-your-project/using-the-api-to-manage-projects#adding-an-item-to-a-project
	var mutation struct {
		AddProjectV2ItemById struct {
			Item struct {
				ID githubql.ID `graphql:"id"`
			} `graphql:"item"`
		} `graphql:"addProjectV2ItemById(input: $input)"`
	}
	input := githubql.AddProjectV2ItemByIdInput{
		ProjectID: projectID,
		ContentID: contentID,
	}

	return c.v4Client.Mutate(ctx, &mutation, input, nil)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
