package tools

// rlTrainingSchema is the tool schema for rl_training.
var rlTrainingSchema = map[string]any{
	"name":        "rl_training",
	"description": "Manage RL training runs via Tinker-Atropos (GRPO/REINFORCE training with WandB monitoring). Requires tinker-atropos submodule at ../tinker-atropos. Actions: list_envs, get_config, edit_config, start, status, stop, results.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action: list_envs | get_config | edit_config | start | status | stop | results",
			},
			"environment": map[string]any{
				"type":        "string",
				"description": "Environment name (required for start/edit_config actions)",
			},
			"config_overrides": map[string]any{
				"type":        "object",
				"description": "Config fields to override (merged with locked defaults). Cannot override locked fields: tokenizer_name, rollout_server_url, use_wandb, max_token_length, etc.",
			},
			"run_id": map[string]any{
				"type":        "string",
				"description": "Run ID (required for status/stop/results actions)",
			},
		},
		"required": []any{"action"},
	},
}

// rlTrainingHandler handles RL training actions.
func rlTrainingHandler(args map[string]any) string {
	action, _ := args["action"].(string)

	switch action {
	case "list_envs":
		return toolResultData(map[string]any{
			"status":  "unavailable",
			"message": "rl_training requires tinker-atropos submodule. Install it at ../tinker-atropos relative to hermes-agent root, then restart.",
			"example": "git submodule add https://github.com/your/tinker-atropos.git ../tinker-atropos",
		})
	case "get_config", "edit_config", "start", "status", "stop", "results":
		return toolResultData(map[string]any{
			"status":  "unavailable",
			"message": "rl_training requires tinker-atropos submodule. See list_envs for setup instructions.",
		})
	default:
		return toolError("unknown action — use: list_envs, get_config, edit_config, start, status, stop, results")
	}
}
