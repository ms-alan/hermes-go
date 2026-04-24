package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// ============================================================================
// Home Assistant Tools — control smart home devices via HA REST API
// ============================================================================
//
// Requires HASS_TOKEN (Long-Lived Access Token from HA profile).
// Optional HASS_URL (default: http://homeassistant.local:8123).
//
// Security: blocked domains prevent SSRF and arbitrary code execution.
// Blocked: shell_command, command_line, python_script, pyscript, hassio, rest_command

// Config
// ---------------------------------------------------------------------------

func hassURL() string {
	if v := os.Getenv("HASS_URL"); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	return "http://homeassistant.local:8123"
}

func hassToken() string {
	return os.Getenv("HASS_TOKEN")
}

func hassCheck() bool {
	return hassToken() != ""
}

func hassHeaders() map[string]string {
	return map[string]string{
		"Authorization":  "Bearer " + hassToken(),
		"Content-Type":    "application/json",
	}
}

// Security: blocked domains that allow arbitrary code/command execution or SSRF.
var blockedDomains = map[string]bool{
	"shell_command":  true,
	"command_line":   true,
	"python_script":  true,
	"pyscript":       true,
	"hassio":        true,
	"rest_command":   true,
}

// Regex for valid HA entity_id (e.g. "light.living_room", "sensor.temp_1")
var entityIDRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*\.[a-z0-9_]+$`)

// Regex for valid service domain/service names (lowercase ASCII + underscore only)
var serviceNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// HTTP client for HA API calls.
var haClient = &http.Client{Timeout: 15 * time.Second}

// ---------------------------------------------------------------------------
// HA API helpers
// ---------------------------------------------------------------------------

func haGet(path string) (map[string]any, error) {
	url := hassURL() + "/api" + path
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range hassHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := haClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HA API %d: %s", resp.StatusCode, string(body))
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		// Some endpoints return arrays
		return nil, nil
	}
	return result, nil
}

func haGetList(path string) ([]map[string]any, error) {
	url := hassURL() + "/api" + path
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range hassHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := haClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HA API %d: %s", resp.StatusCode, string(body))
	}
	var result []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func haPost(path string, payload map[string]any) (any, error) {
	url := hassURL() + "/api" + path
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range hassHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := haClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HA API %d: %s", resp.StatusCode, string(respBody))
	}
	var result any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		// Some responses are empty
		return nil, nil
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Tool schemas
// ---------------------------------------------------------------------------

var haListEntitiesSchema = map[string]any{
	"description": "List Home Assistant entities. Optionally filter by domain (light, switch, climate, sensor, binary_sensor, cover, fan) or area name.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"domain": map[string]any{
				"type":        "string",
				"description": "Entity domain to filter by (e.g. 'light', 'switch', 'climate', 'sensor', 'binary_sensor', 'cover', 'fan', 'media_player'). Omit to list all.",
			},
			"area": map[string]any{
				"type":        "string",
				"description": "Area/room name to filter by (e.g. 'living room', 'kitchen'). Matches against entity friendly names.",
			},
		},
	},
}

var haGetStateSchema = map[string]any{
	"description": "Get the detailed state of a single Home Assistant entity, including all attributes.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"entity_id": map[string]any{
				"type":        "string",
				"description": "The entity ID to query (e.g. 'light.living_room', 'climate.thermostat', 'sensor.temperature').",
			},
		},
		"required": []any{"entity_id"},
	},
}

var haListServicesSchema = map[string]any{
	"description": "List available Home Assistant services (actions) for device control. Use to discover available actions for each domain.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"domain": map[string]any{
				"type":        "string",
				"description": "Filter by domain (e.g. 'light', 'climate', 'switch'). Omit to list all domains.",
			},
		},
	},
}

var haCallServiceSchema = map[string]any{
	"description": "Call a Home Assistant service to control a device. Use ha_list_services to discover available services and their parameters.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"domain": map[string]any{
				"type":        "string",
				"description": "Service domain (e.g. 'light', 'switch', 'climate', 'cover', 'media_player', 'fan', 'scene', 'script').",
			},
			"service": map[string]any{
				"type":        "string",
				"description": "Service name (e.g. 'turn_on', 'turn_off', 'toggle', 'set_temperature', 'set_hvac_mode', 'open_cover', 'close_cover', 'set_volume_level').",
			},
			"entity_id": map[string]any{
				"type":        "string",
				"description": "Target entity ID (e.g. 'light.living_room'). Some services like scene.turn_on may not need this.",
			},
			"data": map[string]any{
				"type":        "string",
				"description": "Additional service data as JSON string. Examples: {\"brightness\": 255, \"color_name\": \"blue\"} for lights, {\"temperature\": 22} for climate, {\"volume_level\": 0.5} for media players.",
			},
		},
		"required": []any{"domain", "service"},
	},
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func haListEntitiesHandler(args map[string]interface{}) string {
	domain := hassStr(args, "domain")
	area := hassStr(args, "area")

	states, err := haGetList("/states")
	if err != nil {
		return toolError("Failed to fetch HA states: " + err.Error())
	}

	// Filter by domain
	if domain != "" {
		prefix := domain + "."
		filtered := make([]map[string]any, 0)
		for _, s := range states {
			if eid, ok := s["entity_id"].(string); ok && strings.HasPrefix(eid, prefix) {
				filtered = append(filtered, s)
			}
		}
		states = filtered
	}

	// Filter by area
	if area != "" {
		areaLower := strings.ToLower(area)
		filtered := make([]map[string]any, 0)
		for _, s := range states {
			attrs, _ := s["attributes"].(map[string]any)
			friendlyName, _ := attrs["friendly_name"].(string)
			areaAttr, _ := attrs["area"].(string)
			if strings.ToLower(friendlyName) == areaLower || strings.ToLower(areaAttr) == areaLower {
				filtered = append(filtered, s)
			}
		}
		states = filtered
	}

	type EntityOut struct {
		EntityID     string `json:"entity_id"`
		State        string `json:"state"`
		FriendlyName string `json:"friendly_name"`
	}
	out := make([]EntityOut, len(states))
	for i, s := range states {
		attrs, _ := s["attributes"].(map[string]any)
		friendlyName, _ := attrs["friendly_name"].(string)
		state, _ := s["state"].(string)
		eid, _ := s["entity_id"].(string)
		out[i] = EntityOut{EntityID: eid, State: state, FriendlyName: friendlyName}
	}
	return toolResultData(map[string]any{"count": len(out), "entities": out})
}

func haGetStateHandler(args map[string]interface{}) string {
	entityID := hassStr(args, "entity_id")
	if entityID == "" {
		return toolError("entity_id is required")
	}
	if !entityIDRe.MatchString(entityID) {
		return toolError("Invalid entity_id format: " + entityID)
	}

	state, err := haGet("/states/" + entityID)
	if err != nil {
		return toolError("Failed to get state: " + err.Error())
	}

	eid, _ := state["entity_id"].(string)
	st, _ := state["state"].(string)
	lastChanged, _ := state["last_changed"].(string)
	lastUpdated, _ := state["last_updated"].(string)
	attrs, _ := state["attributes"].(map[string]any)

	return toolResultData(map[string]any{
		"entity_id":    eid,
		"state":        st,
		"attributes":   attrs,
		"last_changed": lastChanged,
		"last_updated": lastUpdated,
	})
}

func haListServicesHandler(args map[string]interface{}) string {
	domain := hassStr(args, "domain")

	services, err := haGetList("/services")
	if err != nil {
		return toolError("Failed to fetch HA services: " + err.Error())
	}

	type FieldOut struct {
		Description string `json:"description"`
	}
	type SvcOut struct {
		Description string              `json:"description"`
		Fields      map[string]FieldOut `json:"fields,omitempty"`
	}
	type DomainOut struct {
		Domain   string               `json:"domain"`
		Services map[string]SvcOut     `json:"services"`
	}

	var domains []DomainOut
	for _, sd := range services {
		d, _ := sd["domain"].(string)
		if domain != "" && d != domain {
			continue
		}
		svcMap, _ := sd["services"].(map[string]any)
		svcs := make(map[string]SvcOut)
		for name, info := range svcMap {
			infoMap, _ := info.(map[string]any)
			desc, _ := infoMap["description"].(string)
			fieldsMap := make(map[string]FieldOut)
			if fields, ok := infoMap["fields"].(map[string]any); ok {
				for fk, fv := range fields {
					if fvMap, ok := fv.(map[string]any); ok {
						fdesc, _ := fvMap["description"].(string)
						fieldsMap[fk] = FieldOut{Description: fdesc}
					}
				}
			}
			svcs[name] = SvcOut{Description: desc, Fields: fieldsMap}
		}
		domains = append(domains, DomainOut{Domain: d, Services: svcs})
	}

	return toolResultData(map[string]any{"count": len(domains), "domains": domains})
}

func haCallServiceHandler(args map[string]interface{}) string {
	domain := hassStr(args, "domain")
	service := hassStr(args, "service")
	if domain == "" || service == "" {
		return toolError("domain and service are required")
	}

	// Validate format BEFORE blocklist check
	if !serviceNameRe.MatchString(domain) {
		return toolError(fmt.Sprintf("Invalid domain format: %q (only lowercase a-z, 0-9, _ allowed)", domain))
	}
	if !serviceNameRe.MatchString(service) {
		return toolError(fmt.Sprintf("Invalid service format: %q (only lowercase a-z, 0-9, _ allowed)", service))
	}

	// Security: block dangerous domains
	if blockedDomains[domain] {
		var blocked []string
		for k := range blockedDomains {
			blocked = append(blocked, k)
		}
		return toolError(fmt.Sprintf("Service domain %q is blocked for security. Blocked: %v", domain, blocked))
	}

	entityID := hassStr(args, "entity_id")
	if entityID != "" && !entityIDRe.MatchString(entityID) {
		return toolError("Invalid entity_id format: " + entityID)
	}

	// Parse optional data JSON string
	var data map[string]any
	if dataStr := hassStr(args, "data"); dataStr != "" {
		if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
			return toolError("Invalid JSON in data: " + err.Error())
		}
	}

	payload := make(map[string]any)
	if entityID != "" {
		payload["entity_id"] = entityID
	}
	for k, v := range data {
		payload[k] = v
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = ctx

	result, err := haPost("/services/"+domain+"/"+service, payload)
	if err != nil {
		return toolError("Service call failed: " + err.Error())
	}

	type AffectedEntity struct {
		EntityID string `json:"entity_id"`
		State    string `json:"state"`
	}
	var affected []AffectedEntity
	if resultList, ok := result.([]any); ok {
		for _, r := range resultList {
			if rm, ok := r.(map[string]any); ok {
				eid, _ := rm["entity_id"].(string)
				st, _ := rm["state"].(string)
				affected = append(affected, AffectedEntity{EntityID: eid, State: st})
			}
		}
	}

	return toolResultData(map[string]any{
		"success":            true,
		"service":            domain + "." + service,
		"affected_entities": affected,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func hassStr(args map[string]interface{}, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func init() {
	Register("ha_list_entities", "homeassistant", haListEntitiesSchema, haListEntitiesHandler, hassCheck,
		[]string{"HASS_TOKEN"}, false,
		"List HA entities by domain or area", "🏠")
	Register("ha_get_state", "homeassistant", haGetStateSchema, haGetStateHandler, hassCheck,
		[]string{"HASS_TOKEN"}, false,
		"Get HA entity detailed state and attributes", "🏠")
	Register("ha_list_services", "homeassistant", haListServicesSchema, haListServicesHandler, hassCheck,
		[]string{"HASS_TOKEN"}, false,
		"List available HA services per domain", "🏠")
	Register("ha_call_service", "homeassistant", haCallServiceSchema, haCallServiceHandler, hassCheck,
		[]string{"HASS_TOKEN"}, false,
		"Call a HA service to control a device", "🏠")
}
