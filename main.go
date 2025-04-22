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
	// kubernetesOrgName is the name of the Kubernetes GitHub organization.
	kubernetesOrgName = "kubernetes"
	// kubernetesSIGSOrgName is the name of the Kubernetes SIGs GitHub organization.
	kubernetesSIGSOrgName = "kubernetes-sigs"
	// projectNumber is the number of the project to add items to.
	// SIG Auth project board: https://github.com/orgs/kubernetes/projects/116
	projectNumber = 116

	needsTriageColumnName           = "Needs Triage"
	subprojectNeedsTriageColumnName = "Subprojects - Needs Triage"
)

type ghClient struct {
	*github.Client
	v4Client *githubql.Client
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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

	project, err := client.getProject(ctx, kubernetesOrgName, projectNumber)
	must(err)

	// Get the ID of the Status field and the ID of the desired status option (e.g., "Needs Triage").
	// This is used to set the status of kubernetes org items to "Needs Triage" during initial import.
	needsTriageStatusFieldID, needsTriageOptionID, err := getStatusFieldOption(project, needsTriageColumnName)
	must(err)

	// Get the ID of the Status field and the ID of the desired status option (e.g., "Subprojects - Needs Triage").
	// This is used to set the status of subproject items to "Subprojects - Needs Triage" during initial import.
	subprojectStatusFieldID, subprojectNeedsTriageOptionID, err := getStatusFieldOption(project, subprojectNeedsTriageColumnName)
	must(err)

	kubernetesOrgRepos, err := client.listRepos(ctx, kubernetesOrgName)
	must(err)

	for _, repo := range kubernetesOrgRepos {
		fmt.Printf("Looking for issues and PRs in %s/%s\n", kubernetesOrgName, *repo.Name)

		items, err := client.listIssuesAndPullRequests(ctx, kubernetesOrgName, *repo.Name, "sig/auth")
		must(err)

		fmt.Printf("found %d in repo %s/%s\n", len(items), kubernetesOrgName, *repo.Name)
		for _, item := range items {
			fmt.Printf("adding [%d] %q to project\n", *item.Number, *item.Title)
			err = client.addAndUpdateProjectItem(ctx, project.ID, item, needsTriageStatusFieldID, needsTriageOptionID)
			must(err)
		}
	}

	// Get the list of repositories in the kubernetes-sigs organization that have the "k8s-sig-auth" topic.
	// equivalent to the following repo query: https://github.com/search?q=topic%3Ak8s-sig-auth+org%3Akubernetes-sigs&type=Repositories
	kubernetesSIGSRepos, err := client.searchReposByTopic(ctx, "k8s-sig-auth", kubernetesSIGSOrgName)
	must(err)

	for _, repo := range kubernetesSIGSRepos {
		fmt.Printf("Looking for issues and PRs in %s/%s\n", kubernetesSIGSOrgName, *repo.Name)

		items, err := client.listIssuesAndPullRequests(ctx, kubernetesSIGSOrgName, *repo.Name, "")
		must(err)

		fmt.Printf("found %d in repo %s/%s\n", len(items), kubernetesSIGSOrgName, *repo.Name)
		for _, item := range items {
			fmt.Printf("adding [%d] %q to project\n", *item.Number, *item.Title)
			err = client.addAndUpdateProjectItem(ctx, project.ID, item, subprojectStatusFieldID, subprojectNeedsTriageOptionID)
			must(err)
		}
	}
}

// addAndUpdateProjectItem adds an item to a project and updates its status field.
// set to "Needs Triage" during initial import for items in the kubernetes org
// and to "Subprojects - Needs Triage" for items in the kubernetes-sigs org (subprojects).
func (c *ghClient) addAndUpdateProjectItem(ctx context.Context, projectID githubql.ID, item *github.Issue, fieldID, optionID githubql.String) error {
	projectItem, err := c.addProjectV2ItemById(ctx, projectID, *item.NodeID)
	if err != nil {
		return err
	}
	// When the item is added to the project, the status field is not set.
	// The field value is empty, so we need to set it to the desired status.
	if len(projectItem.FieldValueByName.ProjectV2SingleSelectField.Name) == 0 {
		fmt.Printf("updating status field for [%d] %q\n", *item.Number, *item.Title)
		return c.updateProjectItemField(ctx, projectID, projectItem.ID, fieldID, optionID)
	}
	fmt.Printf("status field already set for [%d] %q\n", *item.Number, *item.Title)
	return nil
}

// listRepos lists all repositories in a specific organization.
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

// searchReposByTopic searches for repositories by topic in a specific organization.
func (c *ghClient) searchReposByTopic(ctx context.Context, topic, org string) ([]*github.Repository, error) {
	var allRepos []*github.Repository
	opts := &github.SearchOptions{
		ListOptions: github.ListOptions{PerPage: perPage},
	}
	query := fmt.Sprintf("topic:%s org:%s", topic, org)

	for {
		result, resp, err := c.Search.Repositories(ctx, query, opts)
		if err != nil {
			return nil, err
		}
		allRepos = append(allRepos, result.Repositories...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return allRepos, nil
}

// listIssuesAndPullRequests lists all issues and pull requests in a repository.
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

// ProjectV2Item https://docs.github.com/en/graphql/reference/objects#projectv2item
type ProjectV2Item struct {
	ID               githubql.String
	Project          ProjectV2
	FieldValueByName struct {
		ProjectV2SingleSelectField struct {
			Name githubql.String
		} `graphql:"... on ProjectV2ItemFieldSingleSelectValue"`
	} `graphql:"fieldValueByName(name: \"Status\")"`
}

// ProjectV2 https://docs.github.com/en/graphql/reference/objects#projectv2
type ProjectV2 struct {
	Title  githubql.String
	ID     githubql.String
	Number githubql.Int
	Field  struct {
		ProjectV2SingleSelectField struct {
			ID      githubql.String
			Options []struct {
				ID   githubql.String
				Name githubql.String
			}
		} `graphql:"... on ProjectV2SingleSelectField"`
	} `graphql:"field(name: \"Status\")"` // gather the selection options for the Status field
}

// getProject retrieves a project by its number in the specified organization.
func (c *ghClient) getProject(ctx context.Context, org string, number int) (*ProjectV2, error) {
	var query struct {
		Organization struct {
			ProjectV2 ProjectV2 `graphql:"projectV2(number: $number)"`
		} `graphql:"organization(login: $org)"`
	}

	variables := map[string]interface{}{
		"org":    githubql.String(org),
		"number": githubql.Int(number),
	}

	if err := c.v4Client.Query(ctx, &query, variables); err != nil {
		return nil, err
	}
	if query.Organization.ProjectV2.ID == "" {
		return nil, fmt.Errorf("project %d not found in org %s", number, org)
	}

	return &query.Organization.ProjectV2, nil
}

// getStatusFieldOption retrieves the ID of the Status field and the ID of the desired status option.
func getStatusFieldOption(project *ProjectV2, desired string) (githubql.String, githubql.String, error) {
	field := project.Field.ProjectV2SingleSelectField
	for _, opt := range field.Options {
		if string(opt.Name) == desired {
			return field.ID, opt.ID, nil
		}
	}
	return "", "", fmt.Errorf("status option %q not found", desired)
}

// addProjectV2ItemById adds an item to a project using the GraphQL API.
func (c *ghClient) addProjectV2ItemById(ctx context.Context, projectID, contentID githubql.ID) (*ProjectV2Item, error) {
	// xref: https://docs.github.com/en/issues/planning-and-tracking-with-projects/automating-your-project/using-the-api-to-manage-projects#adding-an-item-to-a-project
	var mutation struct {
		AddProjectV2ItemById struct {
			Item ProjectV2Item
		} `graphql:"addProjectV2ItemById(input: $input)"`
	}
	input := githubql.AddProjectV2ItemByIdInput{
		ProjectID: projectID,
		ContentID: contentID,
	}

	if err := c.v4Client.Mutate(ctx, &mutation, input, nil); err != nil {
		return nil, err
	}
	return &mutation.AddProjectV2ItemById.Item, nil
}

// updateProjectItemField updates the Staus field of a project item.
func (c *ghClient) updateProjectItemField(ctx context.Context, projectID, itemID githubql.ID, fieldID, optionID githubql.String) error {
	var mutation struct {
		UpdateProjectV2ItemFieldValue struct {
			ProjectV2Item struct {
				ID githubql.ID
			} `graphql:"projectV2Item"`
		} `graphql:"updateProjectV2ItemFieldValue(input: $input)"`
	}

	input := githubql.UpdateProjectV2ItemFieldValueInput{
		ProjectID: projectID,
		ItemID:    itemID,
		FieldID:   fieldID,
		Value: githubql.ProjectV2FieldValue{
			SingleSelectOptionID: &optionID,
		},
	}
	return c.v4Client.Mutate(ctx, &mutation, input, nil)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
