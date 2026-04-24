package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const linearEndpoint = "https://api.linear.app/graphql"

var linearSchema = map[string]any{
	"name":        "linear",
	"description": "Manage Linear issues, projects, and teams via the GraphQL API. Supports listing, searching, creating, and updating issues. Requires LINEAR_API_KEY environment variable.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "One of: status, teams, issues, get_issue, create_issue, update_issue, search_issues, workflow_states, labels",
			},
			"team_id": map[string]any{
				"type":        "string",
				"description": "Team UUID (e.g. 'a1b2c3d4-...'). Use with action=issues to filter by team.",
			},
			"team_key": map[string]any{
				"type":        "string",
				"description": "Team key/short-name (e.g. 'ENG'). Use with action=workflow_states.",
			},
			"issue_id": map[string]any{
				"type":        "string",
				"description": "Issue identifier (e.g. 'ENG-123') or UUID.",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "Issue title (for create_issue).",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Issue description in Markdown (for create_issue).",
			},
			"priority": map[string]any{
				"type":        "integer",
				"description": "Priority: 0=None, 1=Urgent, 2=High, 3=Medium, 4=Low (for create_issue, update_issue).",
			},
			"state_id": map[string]any{
				"type":        "string",
				"description": "Workflow state UUID (for update_issue). Get via action=workflow_states.",
			},
			"assignee_id": map[string]any{
				"type":        "string",
				"description": "Assignee user UUID (for create_issue, update_issue). Get via action=teams.",
			},
			"label_ids": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
				"description": "Label UUIDs to apply (for create_issue, update_issue).",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "Search query text (for search_issues).",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Max results to return (default 20, max 250).",
			},
			"assignee_email": map[string]any{
				"type":        "string",
				"description": "Filter issues by assignee email (for action=issues).",
			},
			"state_type": map[string]any{
				"type":        "string",
				"description": "Filter by state type: triage, backlog, unstarted, started, completed, canceled (for action=issues).",
			},
		},
	},
}

func linearHandler(args map[string]any) string {
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return `{"error": "LINEAR_API_KEY environment variable not set"}`
	}

	action, _ := args["action"].(string)
	if action == "" {
		action = "status"
	}

	switch action {
	case "status":
		return linearStatus(apiKey)
	case "teams":
		return linearTeams(apiKey)
	case "issues":
		return linearListIssues(apiKey, args)
	case "get_issue":
		return linearGetIssue(apiKey, args)
	case "create_issue":
		return linearCreateIssue(apiKey, args)
	case "update_issue":
		return linearUpdateIssue(apiKey, args)
	case "search_issues":
		return linearSearchIssues(apiKey, args)
	case "workflow_states":
		return linearWorkflowStates(apiKey, args)
	case "labels":
		return linearLabels(apiKey)
	default:
		return fmt.Sprintf(`{"error": "unknown action: %q, valid actions: status, teams, issues, get_issue, create_issue, update_issue, search_issues, workflow_states, labels"}`, action)
	}
}

func linearDo(apiKey string, query string, variables map[string]any) (string, error) {
	payload := map[string]any{
		"query":     query,
		"variables": variables,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", linearEndpoint, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Check for GraphQL errors
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	if errs, ok := result["errors"].([]any); ok && len(errs) > 0 {
		errMsgs := make([]string, len(errs))
		for i, e := range errs {
			if m, ok := e.(map[string]any); ok {
				errMsgs[i] = fmt.Sprintf("%v", m["message"])
			}
		}
		return "", fmt.Errorf("GraphQL errors: %s", strings.Join(errMsgs, "; "))
	}

	return string(body), nil
}

func linearStatus(apiKey string) string {
	resp, err := linearDo(apiKey, `{ viewer { id name email } }`, nil)
	if err != nil {
		return fmt.Sprintf(`{"error": %s}`, jsonEncode(err.Error()))
	}
	return resp
}

func linearTeams(apiKey string) string {
	resp, err := linearDo(apiKey, `{ teams(first: 50) { nodes { id name key } } }`, nil)
	if err != nil {
		return fmt.Sprintf(`{"error": %s}`, jsonEncode(err.Error()))
	}
	return resp
}

func linearListIssues(apiKey string, args map[string]any) string {
	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
		if limit < 1 {
			limit = 1
		}
		if limit > 250 {
			limit = 250
		}
	}

	vars := map[string]any{"first": limit}

	// Build filter
	var filters []string
	if teamID, ok := args["team_id"].(string); ok && teamID != "" {
		filters = append(filters, fmt.Sprintf(`team: { id: { eq: %q } }`, teamID))
	}
	if email, ok := args["assignee_email"].(string); ok && email != "" {
		filters = append(filters, fmt.Sprintf(`assignee: { email: { eq: %q } }`, email))
	}
	if stateType, ok := args["state_type"].(string); ok && stateType != "" {
		filters = append(filters, fmt.Sprintf(`state: { type: { in: [%q] } }`, stateType))
	}

	query := fmt.Sprintf(`{ issues(first: %d%s) { nodes { identifier title priority state { name type } assignee { name } team { key } url } pageInfo { hasNextPage endCursor } } }`,
		limit, buildFilter(filters))

	resp, err := linearDo(apiKey, query, vars)
	if err != nil {
		return fmt.Sprintf(`{"error": %s}`, jsonEncode(err.Error()))
	}
	return resp
}

func linearGetIssue(apiKey string, args map[string]any) string {
	issueID, _ := args["issue_id"].(string)
	if issueID == "" {
		return `{"error": "issue_id is required for get_issue"}`
	}
	query := `{ issue(id: $id) { id identifier title description priority state { id name type } assignee { id name } team { key } project { name } labels { nodes { name } } comments(first: 10) { nodes { body user { name } createdAt } } url } }`
	vars := map[string]any{"id": issueID}
	resp, err := linearDo(apiKey, query, vars)
	if err != nil {
		return fmt.Sprintf(`{"error": %s}`, jsonEncode(err.Error()))
	}
	return resp
}

func linearCreateIssue(apiKey string, args map[string]any) string {
	teamID, _ := args["team_id"].(string)
	title, _ := args["title"].(string)
	if teamID == "" || title == "" {
		return `{"error": "team_id and title are required for create_issue"}`
	}

	input := map[string]any{
		"teamId": teamID,
		"title":  title,
	}
	if desc, ok := args["description"].(string); ok && desc != "" {
		input["description"] = desc
	}
	if pri, ok := args["priority"].(float64); ok {
		input["priority"] = int(pri)
	}
	if assigneeID, ok := args["assignee_id"].(string); ok && assigneeID != "" {
		input["assigneeId"] = assigneeID
	}
	if labelIDs, ok := args["label_ids"].([]any); ok && len(labelIDs) > 0 {
		ids := make([]string, len(labelIDs))
		for i, v := range labelIDs {
			if s, ok := v.(string); ok {
				ids[i] = s
			}
		}
		input["labelIds"] = ids
	}

	vars := map[string]any{"input": input}
	resp, err := linearDo(apiKey,
		`mutation($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { id identifier title url } } }`,
		vars)
	if err != nil {
		return fmt.Sprintf(`{"error": %s}`, jsonEncode(err.Error()))
	}
	return resp
}

func linearUpdateIssue(apiKey string, args map[string]any) string {
	issueID, _ := args["issue_id"].(string)
	if issueID == "" {
		return `{"error": "issue_id is required for update_issue"}`
	}

	input := map[string]any{}
	if stateID, ok := args["state_id"].(string); ok && stateID != "" {
		input["stateId"] = stateID
	}
	if pri, ok := args["priority"].(float64); ok {
		input["priority"] = int(pri)
	}
	if assigneeID, ok := args["assignee_id"].(string); ok && assigneeID != "" {
		input["assigneeId"] = assigneeID
	}
	if labelIDs, ok := args["label_ids"].([]any); ok {
		ids := make([]string, 0, len(labelIDs))
		for _, v := range labelIDs {
			if s, ok := v.(string); ok {
				ids = append(ids, s)
			}
		}
		input["labelIds"] = ids
	}

	if len(input) == 0 {
		return `{"error": "no fields to update (state_id, priority, assignee_id, or label_ids required)"}`
	}

	vars := map[string]any{"id": issueID, "input": input}
	resp, err := linearDo(apiKey,
		`mutation($id: String!, $input: IssueUpdateInput!) { issueUpdate(id: $id, input: $input) { success issue { identifier state { name type } priority assignee { name } } } }`,
		vars)
	if err != nil {
		return fmt.Sprintf(`{"error": %s}`, jsonEncode(err.Error()))
	}
	return resp
}

func linearSearchIssues(apiKey string, args map[string]any) string {
	query, _ := args["query"].(string)
	if query == "" {
		return `{"error": "query is required for search_issues"}`
	}
	limit := 10
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}
	vars := map[string]any{"query": query, "first": limit}
	resp, err := linearDo(apiKey,
		`query($query: String!, $first: Int!) { issueSearch(query: $query, first: $first) { nodes { identifier title state { name } assignee { name } url } } }`,
		vars)
	if err != nil {
		return fmt.Sprintf(`{"error": %s}`, jsonEncode(err.Error()))
	}
	return resp
}

func linearWorkflowStates(apiKey string, args map[string]any) string {
	teamKey, _ := args["team_key"].(string)
	if teamKey == "" {
		return `{"error": "team_key is required for workflow_states (e.g. 'ENG')"}`
	}
	query := fmt.Sprintf(`{ workflowStates(filter: { team: { key: { eq: %q } } }) { nodes { id name type } } }`, teamKey)
	resp, err := linearDo(apiKey, query, nil)
	if err != nil {
		return fmt.Sprintf(`{"error": %s}`, jsonEncode(err.Error()))
	}
	return resp
}

func linearLabels(apiKey string) string {
	resp, err := linearDo(apiKey, `{ issueLabels(first: 100) { nodes { id name color } } }`, nil)
	if err != nil {
		return fmt.Sprintf(`{"error": %s}`, jsonEncode(err.Error()))
	}
	return resp
}

func buildFilter(filters []string) string {
	if len(filters) == 0 {
		return ""
	}
	return ", filter: { " + strings.Join(filters, ", ") + " }"
}

func jsonEncode(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func init() {
	Register("linear", "productivity",
		linearSchema,
		func(args map[string]any) string { return linearHandler(args) },
		func() bool { return os.Getenv("LINEAR_API_KEY") != "" },
		[]string{"LINEAR_API_KEY"}, // requiresEnv
		false,                      // isAsync
		"Linear issue management — list, create, update, search issues", // description
		"📋",                       // emoji
	)
}
