package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ============================================================================
// Discord Tool — server introspection and management via Discord REST API
// ============================================================================
//
// Requires DISCORD_BOT_TOKEN env var. Uses Discord API v10.
// Only included in the "discord" toolset.
//
// Actions:
// - list_guilds: list servers the bot is in
// - server_info: server details + member counts
// - list_channels: all channels grouped by category
// - channel_info: single channel details
// - list_roles: roles sorted by position
// - member_info: lookup a specific member (requires GUILD_MEMBERS intent)
// - search_members: find members by name prefix (requires GUILD_MEMBERS intent)
// - fetch_messages: recent messages; optional before/after snowflakes
// - list_pins: pinned messages in a channel
// - pin_message: pin a message
// - unpin_message: unpin a message
// - create_thread: create a public thread
// - add_role: assign a role to a member
// - remove_role: remove a role from a member

const discordAPIBase = "https://discord.com/api/v10"

// Channel type names
var channelTypeNames = map[int]string{
	0:  "text",
	2:  "voice",
	4:  "category",
	5:  "announcement",
	10: "announcement_thread",
	11: "public_thread",
	12: "private_thread",
	13: "stage",
	15: "forum",
	16: "media",
}

// DiscordAPIError represents a Discord API error.
type DiscordAPIError struct {
	Status int
	Body   string
}

func (e *DiscordAPIError) Error() string {
	return fmt.Sprintf("Discord API error %d: %s", e.Status, e.Body)
}

// discordClient is the HTTP client for Discord API.
var discordClient = &http.Client{Timeout: 15 * time.Second}

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

func discordToken() string {
	return os.Getenv("DISCORD_BOT_TOKEN")
}

func discordCheck() bool {
	return discordToken() != ""
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func discordRequest(method, path string, params map[string]string, body any) (any, error) {
	apiURL := discordAPIBase + path
	if params != nil {
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		if strings.Contains(path, "?") {
			apiURL += "&" + q.Encode()
		} else {
			apiURL += "?" + q.Encode()
		}
	}

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, apiURL, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bot "+discordToken())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Hermes-Agent (https://github.com/NousResearch/hermes-agent)")

	resp, err := discordClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 204 {
		return nil, nil
	}
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, &DiscordAPIError{Status: resp.StatusCode, Body: string(bodyBytes)}
	}

	if resp.StatusCode == 200 || resp.StatusCode == 201 {
		var result any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, nil // Empty body
		}
		return result, nil
	}
	return nil, nil
}

func discordGet(path string, params map[string]string) (any, error) {
	return discordRequest(http.MethodGet, path, params, nil)
}

func discordPost(path string, body any) (any, error) {
	return discordRequest(http.MethodPost, path, nil, body)
}

func discordPut(path string) error {
	_, err := discordRequest(http.MethodPut, path, nil, nil)
	return err
}

func discordDelete(path string) error {
	_, err := discordRequest(http.MethodDelete, path, nil, nil)
	return err
}

// ---------------------------------------------------------------------------
// Channel helpers
// ---------------------------------------------------------------------------

func channelTypeName(typeID int) string {
	if name, ok := channelTypeNames[typeID]; ok {
		return name
	}
	return fmt.Sprintf("unknown(%d)", typeID)
}

// ---------------------------------------------------------------------------
// Tool schemas
// ---------------------------------------------------------------------------

var discordSchema = map[string]any{
	"name":        "discord",
	"description": "Discord server introspection and management. List servers, channels, roles, members. Read messages, pins. Manage threads and roles.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action to perform",
				"enum": []any{
					"list_guilds", "server_info", "list_channels", "channel_info",
					"list_roles", "member_info", "search_members",
					"fetch_messages", "list_pins", "pin_message", "unpin_message",
					"create_thread", "add_role", "remove_role",
				},
			},
			"guild_id":     map[string]any{"type": "string", "description": "Guild/server ID"},
			"channel_id":   map[string]any{"type": "string", "description": "Channel ID"},
			"user_id":      map[string]any{"type": "string", "description": "User ID"},
			"role_id":      map[string]any{"type": "string", "description": "Role ID"},
			"message_id":   map[string]any{"type": "string", "description": "Message ID"},
			"name":         map[string]any{"type": "string", "description": "Name (for create_thread)"},
			"query":        map[string]any{"type": "string", "description": "Search query (for search_members)"},
			"limit":        map[string]any{"type": "integer", "description": "Max results (default: 50, max: 100)", "default": 50},
			"before":       map[string]any{"type": "string", "description": "Snowflake to fetch messages before"},
			"after":        map[string]any{"type": "string", "description": "Snowflake to fetch messages after"},
		},
		"required": []any{"action"},
	},
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

var discordSnowflakeRe = regexp.MustCompile(`^\d{17,20}$`)

func discordHandler(args map[string]any) string {
	action, _ := args["action"].(string)
	action = strings.TrimSpace(action)

	switch action {
	case "list_guilds":
		return discordListGuilds()
	case "server_info":
		return discordServerInfo(discordStr(args, "guild_id"))
	case "list_channels":
		return discordListChannels(discordStr(args, "guild_id"))
	case "channel_info":
		return discordChannelInfo(discordStr(args, "channel_id"))
	case "list_roles":
		return discordListRoles(discordStr(args, "guild_id"))
	case "member_info":
		return discordMemberInfo(discordStr(args, "guild_id"), discordStr(args, "user_id"))
	case "search_members":
		return discordSearchMembers(discordStr(args, "guild_id"), discordStr(args, "query"), discordInt(args, "limit", 20))
	case "fetch_messages":
		return discordFetchMessages(discordStr(args, "channel_id"), discordInt(args, "limit", 50), discordStr(args, "before"), discordStr(args, "after"))
	case "list_pins":
		return discordListPins(discordStr(args, "channel_id"))
	case "pin_message":
		return discordPinMessage(discordStr(args, "channel_id"), discordStr(args, "message_id"))
	case "unpin_message":
		return discordUnpinMessage(discordStr(args, "channel_id"), discordStr(args, "message_id"))
	case "create_thread":
		return discordCreateThread(discordStr(args, "channel_id"), discordStr(args, "name"), discordStr(args, "message_id"), discordInt(args, "auto_archive_duration", 1440))
	case "add_role":
		return discordAddRole(discordStr(args, "guild_id"), discordStr(args, "user_id"), discordStr(args, "role_id"))
	case "remove_role":
		return discordRemoveRole(discordStr(args, "guild_id"), discordStr(args, "user_id"), discordStr(args, "role_id"))
	default:
		return toolError("Unknown action: " + action + ". Valid: list_guilds, server_info, list_channels, channel_info, list_roles, member_info, search_members, fetch_messages, list_pins, pin_message, unpin_message, create_thread, add_role, remove_role")
	}
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

func discordListGuilds() string {
	result, err := discordGet("/users/@me/guilds", nil)
	if err != nil {
		return toolError("Failed to list guilds: " + err.Error())
	}
	guilds, ok := result.([]any)
	if !ok {
		return toolError("Unexpected guilds response format")
	}
	type GuildOut struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Icon        string `json:"icon"`
		Owner       bool   `json:"owner"`
		Permissions int    `json:"permissions"`
	}
	out := make([]GuildOut, len(guilds))
	for i, g := range guilds {
		gm, _ := g.(map[string]any)
		out[i] = GuildOut{
			ID:          discordStrFrom(gm, "id"),
			Name:        discordStrFrom(gm, "name"),
			Icon:        discordStrFrom(gm, "icon"),
			Owner:       discordBoolFrom(gm, "owner"),
			Permissions: discordIntFrom(gm, "permissions"),
		}
	}
	return toolResultData(map[string]any{"guilds": out, "count": len(out)})
}

func discordServerInfo(guildID string) string {
	if guildID == "" {
		return toolError("guild_id is required")
	}
	result, err := discordGet("/guilds/"+guildID, map[string]string{"with_counts": "true"})
	if err != nil {
		return toolError("Failed to get server info: " + err.Error())
	}
	g, ok := result.(map[string]any)
	if !ok {
		return toolError("Unexpected server info response format")
	}
	return toolResultData(map[string]any{
		"id":                       discordStrFrom(g, "id"),
		"name":                     discordStrFrom(g, "name"),
		"description":              discordStrFrom(g, "description"),
		"icon":                     discordStrFrom(g, "icon"),
		"owner_id":                 discordStrFrom(g, "owner_id"),
		"member_count":             discordIntFrom(g, "approximate_member_count"),
		"online_count":             discordIntFrom(g, "approximate_presence_count"),
		"features":                 g["features"],
		"premium_tier":             g["premium_tier"],
		"premium_subscription_count": g["premium_subscription_count"],
		"verification_level":        g["verification_level"],
	})
}

func discordListChannels(guildID string) string {
	if guildID == "" {
		return toolError("guild_id is required")
	}
	result, err := discordGet("/guilds/"+guildID+"/channels", nil)
	if err != nil {
		return toolError("Failed to list channels: " + err.Error())
	}
	channels, ok := result.([]any)
	if !ok {
		return toolError("Unexpected channels response format")
	}

	categories := make(map[string]map[string]any)
	var uncategorized []map[string]any

	for _, ch := range channels {
		cm, _ := ch.(map[string]any)
		chType := discordIntFrom(cm, "type")
		if chType == 4 { // category
			id := discordStrFrom(cm, "id")
			categories[id] = map[string]any{
				"id":       id,
				"name":     discordStrFrom(cm, "name"),
				"position": discordIntFrom(cm, "position"),
				"channels": []map[string]any{},
			}
		}
	}

	for _, ch := range channels {
		cm, _ := ch.(map[string]any)
		chType := discordIntFrom(cm, "type")
		if chType == 4 {
			continue
		}
		entry := map[string]any{
			"id":       discordStrFrom(cm, "id"),
			"name":     discordStrFrom(cm, "name"),
			"type":     channelTypeName(chType),
			"position": discordIntFrom(cm, "position"),
			"topic":    discordStrFrom(cm, "topic"),
			"nsfw":     discordBoolFrom(cm, "nsfw"),
		}
		parent := discordStrFrom(cm, "parent_id")
		if parent != "" && categories[parent] != nil {
			cats := categories[parent]["channels"].([]map[string]any)
			categories[parent]["channels"] = append(cats, entry)
		} else {
			uncategorized = append(uncategorized, entry)
		}
	}

	// Sort categories by position
	var sortedCats []map[string]any
	for _, v := range categories {
		sortedCats = append(sortedCats, v)
	}
	sort.Slice(sortedCats, func(i, j int) bool {
		return sortedCats[i]["position"].(int) < sortedCats[j]["position"].(int)
	})

	type ChannelGroup struct {
		Category any `json:"category"`
		Channels []map[string]any `json:"channels"`
	}
	var groups []ChannelGroup
	if len(uncategorized) > 0 {
		sort.Slice(uncategorized, func(i, j int) bool {
			return uncategorized[i]["position"].(int) < uncategorized[j]["position"].(int)
		})
		groups = append(groups, ChannelGroup{Category: nil, Channels: uncategorized})
	}
	for _, cat := range sortedCats {
		chans := cat["channels"].([]map[string]any)
		sort.Slice(chans, func(i, j int) bool {
			return chans[i]["position"].(int) < chans[j]["position"].(int)
		})
		groups = append(groups, ChannelGroup{
			Category: map[string]any{"id": cat["id"], "name": cat["name"]},
			Channels: chans,
		})
	}

	total := 0
	for _, g := range groups {
		total += len(g.Channels)
	}
	return toolResultData(map[string]any{"channel_groups": groups, "total_channels": total})
}

func discordChannelInfo(channelID string) string {
	if channelID == "" {
		return toolError("channel_id is required")
	}
	result, err := discordGet("/channels/"+channelID, nil)
	if err != nil {
		return toolError("Failed to get channel info: " + err.Error())
	}
	ch, ok := result.(map[string]any)
	if !ok {
		return toolError("Unexpected channel info response format")
	}
	return toolResultData(map[string]any{
		"id":                  discordStrFrom(ch, "id"),
		"name":                discordStrFrom(ch, "name"),
		"type":                channelTypeName(discordIntFrom(ch, "type")),
		"guild_id":            discordStrFrom(ch, "guild_id"),
		"topic":               discordStrFrom(ch, "topic"),
		"nsfw":                discordBoolFrom(ch, "nsfw"),
		"position":            ch["position"],
		"parent_id":           discordStrFrom(ch, "parent_id"),
		"rate_limit_per_user": discordIntFrom(ch, "rate_limit_per_user"),
		"last_message_id":     discordStrFrom(ch, "last_message_id"),
	})
}

func discordListRoles(guildID string) string {
	if guildID == "" {
		return toolError("guild_id is required")
	}
	result, err := discordGet("/guilds/"+guildID+"/roles", nil)
	if err != nil {
		return toolError("Failed to list roles: " + err.Error())
	}
	roles, ok := result.([]any)
	if !ok {
		return toolError("Unexpected roles response format")
	}
	// Sort by position descending (highest first)
	sort.Slice(roles, func(i, j int) bool {
		ri, _ := roles[i].(map[string]any)
		rj, _ := roles[j].(map[string]any)
		return discordIntFrom(ri, "position") > discordIntFrom(rj, "position")
	})
	type RoleOut struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Color       string `json:"color"`
		Position    int    `json:"position"`
		Mentionable bool   `json:"mentionable"`
		Managed     bool   `json:"managed"`
		MemberCount int    `json:"member_count"`
		Hoist       bool   `json:"hoist"`
	}
	out := make([]RoleOut, len(roles))
	for i, r := range roles {
		rm := r.(map[string]any)
		color := discordIntFrom(rm, "color")
		colorHex := ""
		if color > 0 {
			colorHex = fmt.Sprintf("#%06x", color)
		}
		out[i] = RoleOut{
			ID:          discordStrFrom(rm, "id"),
			Name:        discordStrFrom(rm, "name"),
			Color:       colorHex,
			Position:    discordIntFrom(rm, "position"),
			Mentionable: discordBoolFrom(rm, "mentionable"),
			Managed:     discordBoolFrom(rm, "managed"),
			MemberCount: discordIntFrom(rm, "member_count"),
			Hoist:       discordBoolFrom(rm, "hoist"),
		}
	}
	return toolResultData(map[string]any{"roles": out, "count": len(out)})
}

func discordMemberInfo(guildID, userID string) string {
	if guildID == "" || userID == "" {
		return toolError("guild_id and user_id are required")
	}
	result, err := discordGet("/guilds/"+guildID+"/members/"+userID, nil)
	if err != nil {
		return toolError("Failed to get member info: " + err.Error())
	}
	m, ok := result.(map[string]any)
	if !ok {
		return toolError("Unexpected member info response format")
	}
	user, _ := m["user"].(map[string]any)
	return toolResultData(map[string]any{
		"user_id":      discordStrFrom(user, "id"),
		"username":     discordStrFrom(user, "username"),
		"display_name": discordStrFrom(user, "global_name"),
		"nickname":     discordStrFrom(m, "nick"),
		"avatar":       discordStrFrom(user, "avatar"),
		"bot":          discordBoolFrom(user, "bot"),
		"roles":        m["roles"],
		"joined_at":    discordStrFrom(m, "joined_at"),
		"premium_since": discordStrFrom(m, "premium_since"),
	})
}

func discordSearchMembers(guildID, query string, limit int) string {
	if guildID == "" {
		return toolError("guild_id is required")
	}
	if query == "" {
		return toolError("query is required for search_members")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	result, err := discordGet("/guilds/"+guildID+"/members/search", map[string]string{"query": query, "limit": strconv.Itoa(limit)})
	if err != nil {
		return toolError("Failed to search members: " + err.Error())
	}
	members, ok := result.([]any)
	if !ok {
		return toolError("Unexpected members search response format")
	}
	type MemberOut struct {
		UserID      string `json:"user_id"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Nickname    string `json:"nickname"`
		Bot         bool   `json:"bot"`
		Roles       []string `json:"roles"`
	}
	out := make([]MemberOut, len(members))
	for i, m := range members {
		mm := m.(map[string]any)
		user, _ := mm["user"].(map[string]any)
		out[i] = MemberOut{
			UserID:      discordStrFrom(user, "id"),
			Username:    discordStrFrom(user, "username"),
			DisplayName: discordStrFrom(user, "global_name"),
			Nickname:    discordStrFrom(mm, "nick"),
			Bot:         discordBoolFrom(user, "bot"),
		}
		if roles, ok := mm["roles"].([]any); ok {
			for _, r := range roles {
				if rs, ok := r.(string); ok {
					out[i].Roles = append(out[i].Roles, rs)
				}
			}
		}
	}
	return toolResultData(map[string]any{"members": out, "count": len(out)})
}

func discordFetchMessages(channelID string, limit int, before, after string) string {
	if channelID == "" {
		return toolError("channel_id is required")
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	params := map[string]string{"limit": strconv.Itoa(limit)}
	if before != "" {
		params["before"] = before
	}
	if after != "" {
		params["after"] = after
	}
	result, err := discordGet("/channels/"+channelID+"/messages", params)
	if err != nil {
		return toolError("Failed to fetch messages: " + err.Error())
	}
	messages, ok := result.([]any)
	if !ok {
		return toolError("Unexpected messages response format")
	}
	type AttachmentOut struct {
		Filename string `json:"filename"`
		URL      string `json:"url"`
		Size     int    `json:"size"`
	}
	type ReactionOut struct {
		Emoji  string `json:"emoji"`
		Count  int    `json:"count"`
	}
	type AuthorOut struct {
		ID          string `json:"id"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Bot         bool   `json:"bot"`
	}
	type MessageOut struct {
		ID               string        `json:"id"`
		Content          string        `json:"content"`
		Author           AuthorOut     `json:"author"`
		Timestamp        string        `json:"timestamp"`
		EditedTimestamp  string        `json:"edited_timestamp"`
		Attachments      []AttachmentOut `json:"attachments"`
		Reactions        []ReactionOut  `json:"reactions"`
		Pinned           bool          `json:"pinned"`
	}
	out := make([]MessageOut, len(messages))
	for i, msg := range messages {
		msgm, _ := msg.(map[string]any)
		author, _ := msgm["author"].(map[string]any)
		out[i] = MessageOut{
			ID:              discordStrFrom(msgm, "id"),
			Content:         discordStrFrom(msgm, "content"),
			Author:          AuthorOut{ID: discordStrFrom(author, "id"), Username: discordStrFrom(author, "username"), DisplayName: discordStrFrom(author, "global_name"), Bot: discordBoolFrom(author, "bot")},
			Timestamp:       discordStrFrom(msgm, "timestamp"),
			EditedTimestamp: discordStrFrom(msgm, "edited_timestamp"),
			Pinned:         discordBoolFrom(msgm, "pinned"),
		}
		if attachments, ok := msgm["attachments"].([]any); ok {
			for _, a := range attachments {
				am := a.(map[string]any)
				out[i].Attachments = append(out[i].Attachments, AttachmentOut{
					Filename: discordStrFrom(am, "filename"),
					URL:      discordStrFrom(am, "url"),
					Size:     discordIntFrom(am, "size"),
				})
			}
		}
		if reactions, ok := msgm["reactions"].([]any); ok {
			for _, r := range reactions {
				rm := r.(map[string]any)
				emoji, _ := rm["emoji"].(map[string]any)
				out[i].Reactions = append(out[i].Reactions, ReactionOut{
					Emoji: discordStrFrom(emoji, "name"),
					Count: discordIntFrom(rm, "count"),
				})
			}
		}
	}
	return toolResultData(map[string]any{"messages": out, "count": len(out)})
}

func discordListPins(channelID string) string {
	if channelID == "" {
		return toolError("channel_id is required")
	}
	result, err := discordGet("/channels/"+channelID+"/pins", nil)
	if err != nil {
		return toolError("Failed to list pins: " + err.Error())
	}
	messages, ok := result.([]any)
	if !ok {
		return toolError("Unexpected pins response format")
	}
	type PinOut struct {
		ID        string `json:"id"`
		Content   string `json:"content"`
		Author    string `json:"author"`
		Timestamp string `json:"timestamp"`
	}
	out := make([]PinOut, len(messages))
	for i, msg := range messages {
		msgm, _ := msg.(map[string]any)
		author, _ := msgm["author"].(map[string]any)
		content := discordStrFrom(msgm, "content")
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		out[i] = PinOut{
			ID:        discordStrFrom(msgm, "id"),
			Content:   content,
			Author:    discordStrFrom(author, "username"),
			Timestamp: discordStrFrom(msgm, "timestamp"),
		}
	}
	return toolResultData(map[string]any{"pinned_messages": out, "count": len(out)})
}

func discordPinMessage(channelID, messageID string) string {
	if channelID == "" || messageID == "" {
		return toolError("channel_id and message_id are required")
	}
	if err := discordPut("/channels/" + channelID + "/pins/" + messageID); err != nil {
		return toolError("Failed to pin message: " + err.Error())
	}
	return toolResultData(map[string]any{"success": true, "message": "Message " + messageID + " pinned."})
}

func discordUnpinMessage(channelID, messageID string) string {
	if channelID == "" || messageID == "" {
		return toolError("channel_id and message_id are required")
	}
	if err := discordDelete("/channels/" + channelID + "/pins/" + messageID); err != nil {
		return toolError("Failed to unpin message: " + err.Error())
	}
	return toolResultData(map[string]any{"success": true, "message": "Message " + messageID + " unpinned."})
}

func discordCreateThread(channelID, name, messageID string, autoArchiveDuration int) string {
	if channelID == "" || name == "" {
		return toolError("channel_id and name are required")
	}
	path := "/channels/" + channelID + "/threads"
	var body map[string]any
	if messageID != "" {
		path += "/messages/" + messageID
		body = map[string]any{"name": name, "auto_archive_duration": autoArchiveDuration}
	} else {
		body = map[string]any{"name": name, "auto_archive_duration": autoArchiveDuration, "type": 11}
	}
	result, err := discordPost(path, body)
	if err != nil {
		return toolError("Failed to create thread: " + err.Error())
	}
	thread, ok := result.(map[string]any)
	if !ok {
		return toolError("Unexpected thread creation response")
	}
	return toolResultData(map[string]any{
		"success":   true,
		"thread_id": discordStrFrom(thread, "id"),
		"name":      discordStrFrom(thread, "name"),
	})
}

func discordAddRole(guildID, userID, roleID string) string {
	if guildID == "" || userID == "" || roleID == "" {
		return toolError("guild_id, user_id, and role_id are required")
	}
	path := fmt.Sprintf("/guilds/%s/members/%s/roles/%s", guildID, userID, roleID)
	if err := discordPut(path); err != nil {
		return toolError("Failed to add role: " + err.Error())
	}
	return toolResultData(map[string]any{"success": true, "message": "Role " + roleID + " added to user " + userID})
}

func discordRemoveRole(guildID, userID, roleID string) string {
	if guildID == "" || userID == "" || roleID == "" {
		return toolError("guild_id, user_id, and role_id are required")
	}
	path := fmt.Sprintf("/guilds/%s/members/%s/roles/%s", guildID, userID, roleID)
	if err := discordDelete(path); err != nil {
		return toolError("Failed to remove role: " + err.Error())
	}
	return toolResultData(map[string]any{"success": true, "message": "Role " + roleID + " removed from user " + userID})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func discordStr(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func discordInt(args map[string]any, key string, defaultVal int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	if v, ok := args[key].(int); ok {
		return v
	}
	return defaultVal
}

func discordStrFrom(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func discordIntFrom(m map[string]any, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	if v, ok := m[key].(int); ok {
		return v
	}
	return 0
}

func discordBoolFrom(m map[string]any, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func init() {
	Register("discord", "discord", discordSchema, discordHandler, discordCheck,
		[]string{"DISCORD_BOT_TOKEN"}, false,
		"Discord server introspection and management", "💬")
}
