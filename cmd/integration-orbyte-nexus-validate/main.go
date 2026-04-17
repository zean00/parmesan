package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/template"
	"time"
)

type scriptTemplate struct {
	Name                          string   `json:"name"`
	ComplaintPrompt               string   `json:"complaint_prompt"`
	ComplaintExpectContains       []string `json:"complaint_expect_contains"`
	ProactiveUpdateExpectContains []string `json:"proactive_update_expect_contains"`
	ProductPrompt                 string   `json:"product_prompt"`
	ProductExpectContains         []string `json:"product_expect_contains"`
}

type crmManifest struct {
	Customer struct {
		PartyID string `json:"party_id"`
		Name    string `json:"name"`
	} `json:"customer"`
	Queue struct {
		Code string `json:"code"`
	} `json:"queue"`
}

type scenarioManifest struct {
	Entities map[string]map[string]any `json:"entities"`
}

type webChatItem struct {
	ID   string            `json:"id"`
	Role string            `json:"role,omitempty"`
	Text string            `json:"text,omitempty"`
	Meta map[string]string `json:"meta,omitempty"`
}

type validationReport struct {
	Name                     string            `json:"name"`
	NexusGatewaySessionID    string            `json:"nexus_gateway_session_id"`
	ProductGatewaySessionID  string            `json:"product_gateway_session_id,omitempty"`
	NexusUserID              string            `json:"nexus_user_id"`
	ParmesanSessionID        string            `json:"parmesan_session_id"`
	ProductParmesanSessionID string            `json:"product_parmesan_session_id,omitempty"`
	ComplaintReply           string            `json:"complaint_reply"`
	ProductReply             string            `json:"product_reply"`
	ProactiveUpdate          string            `json:"proactive_update"`
	DelegatedAgent           map[string]any    `json:"delegated_agent,omitempty"`
	SessionWatches           []map[string]any  `json:"session_watches,omitempty"`
	LearnedPreferences       []map[string]any  `json:"learned_preferences,omitempty"`
	ResolvedTicket           map[string]any    `json:"resolved_ticket,omitempty"`
	LeadSearchResult         map[string]any    `json:"lead_search_result,omitempty"`
	Transcript               []webChatItem     `json:"transcript,omitempty"`
	ValidationChecks         map[string]bool   `json:"validation_checks"`
	ConversationInputs       map[string]string `json:"conversation_inputs"`
}

type httpJSONClient struct {
	baseURL string
	client  *http.Client
	headers map[string]string
}

type providerSpec struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	URI  string `json:"uri"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		nexusBaseURL         = flag.String("nexus-base-url", "http://127.0.0.1:18080", "Nexus base URL")
		nexusAdminBaseURL    = flag.String("nexus-admin-base-url", "", "Nexus admin base URL; defaults to nexus-base-url when unset")
		nexusAdminToken      = flag.String("nexus-admin-token", "", "Optional Nexus admin bearer token")
		parmesanBaseURL      = flag.String("parmesan-base-url", "http://127.0.0.1:18090", "Parmesan API base URL")
		parmesanOperatorKey  = flag.String("parmesan-operator-key", "", "Parmesan operator API key")
		orbyteFullMCPURL     = flag.String("orbyte-full-mcp-url", "http://127.0.0.1:18110/mcp", "Orbyte full MCP URL")
		orbyteMinimalMCPURL  = flag.String("orbyte-minimal-mcp-url", "http://127.0.0.1:18111/mcp", "Orbyte minimal MCP URL")
		agentID              = flag.String("agent-id", "agent_orbyte_nexus_validation", "Parmesan agent id")
		crmManifestPath      = flag.String("crm-manifest", "", "Path to Orbyte full CRM seed manifest JSON")
		crmManifestMinimal   = flag.String("crm-manifest-minimal", "", "Path to Orbyte minimal CRM seed manifest JSON; defaults to --crm-manifest")
		showcaseManifestPath = flag.String("showcase-manifest", "", "Path to Orbyte showcase seed manifest JSON")
		scriptPath           = flag.String("script", "integrations/orbyte_nexus/conversations/integrated_validation.json.tmpl", "Path to the validation conversation template")
		email                = flag.String("email", "validation@example.com", "Nexus webchat email for dev login")
		reportOut            = flag.String("report-out", "", "Path to write the validation report JSON")
		timeout              = flag.Duration("timeout", 5*time.Minute, "Overall wait timeout for each conversational checkpoint")
	)
	flag.Parse()

	if strings.TrimSpace(*parmesanOperatorKey) == "" {
		return errors.New("parmesan-operator-key is required")
	}
	if strings.TrimSpace(*crmManifestPath) == "" {
		return errors.New("crm-manifest is required")
	}
	if strings.TrimSpace(*showcaseManifestPath) == "" {
		return errors.New("showcase-manifest is required")
	}

	crmSeed, err := loadCRMManifest(*crmManifestPath)
	if err != nil {
		return err
	}
	minimalSeedPath := strings.TrimSpace(*crmManifestMinimal)
	if minimalSeedPath == "" {
		minimalSeedPath = *crmManifestPath
	}
	crmSeedMinimal, err := loadCRMManifest(minimalSeedPath)
	if err != nil {
		return err
	}
	showcaseSeed, err := loadScenarioManifest(*showcaseManifestPath)
	if err != nil {
		return err
	}
	productCustomer := strings.TrimSpace(crmSeed.Customer.Name)
	if productCustomer == "" {
		return fmt.Errorf("crm manifest customer name is empty in %q", *crmManifestPath)
	}
	complaintCustomer := strings.TrimSpace(crmSeedMinimal.Customer.Name)
	if complaintCustomer == "" {
		return fmt.Errorf("crm manifest customer name is empty in %q", minimalSeedPath)
	}
	complaintPartyID := strings.TrimSpace(crmSeedMinimal.Customer.PartyID)
	if complaintPartyID == "" {
		return fmt.Errorf("crm manifest customer party id is empty in %q", minimalSeedPath)
	}
	complaintQueueCode := strings.TrimSpace(crmSeedMinimal.Queue.Code)
	if complaintQueueCode == "" {
		return fmt.Errorf("crm manifest queue code is empty in %q", minimalSeedPath)
	}
	complaintProductName := chooseProductName(showcaseSeed)
	if complaintProductName == "" {
		return errors.New("unable to determine seeded product name from showcase manifest")
	}

	fullMCP := &httpJSONClient{baseURL: strings.TrimRight(*orbyteFullMCPURL, "/"), client: &http.Client{Timeout: 30 * time.Second}}
	minimalMCP := &httpJSONClient{baseURL: strings.TrimRight(*orbyteMinimalMCPURL, "/"), client: &http.Client{Timeout: 30 * time.Second}}

	script, err := loadScript(*scriptPath, map[string]string{
		"ComplaintCustomerName":    complaintCustomer,
		"ComplaintCustomerPartyID": complaintPartyID,
		"ComplaintQueueCode":       complaintQueueCode,
		"ProductCustomerName":      productCustomer,
		"ComplaintProductName":     complaintProductName,
		"ProductName":              complaintProductName,
	})
	if err != nil {
		return err
	}

	nexus := &httpJSONClient{baseURL: strings.TrimRight(*nexusBaseURL, "/"), client: &http.Client{Timeout: 30 * time.Second}}
	adminBaseURL := strings.TrimSpace(*nexusAdminBaseURL)
	if adminBaseURL == "" {
		adminBaseURL = *nexusBaseURL
	}
	parmesan := &httpJSONClient{
		baseURL: strings.TrimRight(*parmesanBaseURL, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
		headers: map[string]string{"Authorization": "Bearer " + strings.TrimSpace(*parmesanOperatorKey)},
	}
	nexusAdmin := &httpJSONClient{
		baseURL: strings.TrimRight(adminBaseURL, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
	}
	if strings.TrimSpace(*nexusAdminToken) != "" {
		nexusAdmin.headers = map[string]string{"Authorization": "Bearer " + strings.TrimSpace(*nexusAdminToken)}
	}
	if err := ensureProviderCatalog(parmesan, []providerSpec{
		{ID: "orbyte_full", Name: "Orbyte Full MCP", Kind: "mcp_remote", URI: strings.TrimSpace(*orbyteFullMCPURL)},
		{ID: "orbyte_minimal", Name: "Orbyte Minimal MCP", Kind: "mcp_remote", URI: strings.TrimSpace(*orbyteMinimalMCPURL)},
	}, []string{
		"commercial_core.item.search",
		"commercial_core.item.get",
		"crm.customer.summary",
		"crm.lead.find_or_create_for_product_interest",
		"crm.ticket.get",
	}, 90*time.Second); err != nil {
		return err
	}

	login, err := nexusDevLogin(nexus, *email)
	if err != nil {
		return err
	}
	seen := map[string]struct{}{}
	seenParmesan := map[string]struct{}{}
	for _, item := range login.Items {
		seen[seenKey(item)] = struct{}{}
	}

	if err := nexusSendMessage(nexus, login, script.ComplaintPrompt); err != nil {
		return err
	}
	parmesanSessionID, err := lookupParmesanSessionID(nexusAdmin, login.SessionID, 45*time.Second)
	if err != nil {
		parmesanSessionID, err = latestParmesanSessionID(parmesan, *agentID)
		if err != nil {
			return err
		}
	}
	complaintReply, transcript, err := waitForAssistantMessage(nexus, login, parmesan, parmesanSessionID, seen, seenParmesan, script.ComplaintExpectContains, *timeout)
	if err != nil {
		return fmt.Errorf("wait for complaint reply: %w", err)
	}
	delegated, err := findDelegatedAgentResult(parmesan, parmesanSessionID)
	if err != nil {
		return err
	}
	ticketID := strings.TrimSpace(stringValue(delegated["ticket_id"]))
	if ticketID == "" {
		return fmt.Errorf("delegated complaint result did not include ticket_id: %#v", delegated)
	}

	if _, err := callOrbyteMinimalResolve(minimalMCP, ticketID); err != nil {
		return err
	}
	proactiveUpdate, transcript, err := waitForAssistantMessage(nexus, login, parmesan, parmesanSessionID, seen, seenParmesan, script.ProactiveUpdateExpectContains, *timeout)
	if err != nil {
		return fmt.Errorf("wait for proactive ticket update: %w", err)
	}

	productLogin, err := nexusDevLogin(nexus, *email)
	if err != nil {
		return err
	}
	productSeen := map[string]struct{}{}
	productSeenParmesan := map[string]struct{}{}
	for _, item := range productLogin.Items {
		productSeen[seenKey(item)] = struct{}{}
	}
	if err := nexusSendMessage(nexus, productLogin, script.ProductPrompt); err != nil {
		return err
	}
	productParmesanSessionID, err := lookupParmesanSessionID(nexusAdmin, productLogin.SessionID, 45*time.Second)
	if err != nil {
		productParmesanSessionID, err = latestParmesanSessionID(parmesan, *agentID)
		if err != nil {
			return err
		}
	}
	productReply, productTranscript, err := waitForAssistantMessage(nexus, productLogin, parmesan, productParmesanSessionID, productSeen, productSeenParmesan, script.ProductExpectContains, *timeout)
	if err != nil {
		return fmt.Errorf("wait for product reply: %w", err)
	}
	transcript = append(transcript, productTranscript...)

	lifecycle, err := getParmesanLifecycle(parmesan, parmesanSessionID)
	if err != nil {
		return err
	}
	preferences, err := getParmesanPreferences(parmesan, *agentID, *email)
	if err != nil {
		return err
	}
	leadSearch, err := callOrbyteLeadSearch(fullMCP, productCustomer)
	if err != nil {
		return err
	}

	report := validationReport{
		Name:                     script.Name,
		NexusGatewaySessionID:    login.SessionID,
		ProductGatewaySessionID:  productLogin.SessionID,
		NexusUserID:              login.UserID,
		ParmesanSessionID:        parmesanSessionID,
		ProductParmesanSessionID: productParmesanSessionID,
		ComplaintReply:           complaintReply.Text,
		ProductReply:             productReply.Text,
		ProactiveUpdate:          proactiveUpdate.Text,
		DelegatedAgent:           delegated,
		SessionWatches:           lifecycle.Watches,
		LearnedPreferences:       preferences,
		ResolvedTicket:           map[string]any{"ticket_id": ticketID},
		LeadSearchResult:         leadSearch,
		Transcript:               transcript,
		ConversationInputs: map[string]string{
			"complaint_prompt":   script.ComplaintPrompt,
			"product_prompt":     script.ProductPrompt,
			"complaint_customer": complaintCustomer,
			"product_customer":   productCustomer,
			"product_name":       complaintProductName,
		},
		ValidationChecks: map[string]bool{
			"delegated_ticket_created": ticketID != "",
			"watch_created":            len(lifecycle.Watches) > 0,
			"proactive_update_sent":    strings.TrimSpace(proactiveUpdate.Text) != "",
			"product_response_sent":    strings.TrimSpace(productReply.Text) != "" && containsAllFold(productReply.Text, []string{complaintProductName}),
			"learned_preferred_name":   hasPreference(preferences, "preferred_name", "Rina"),
			"learned_contact_channel":  hasPreference(preferences, "contact_channel", "email"),
			"lead_found":               nestedInt(leadSearch, "total") > 0,
		},
	}

	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if strings.TrimSpace(*reportOut) != "" {
		if err := os.WriteFile(*reportOut, raw, 0o644); err != nil {
			return err
		}
	}
	fmt.Println(string(raw))
	return nil
}

type nexusLogin struct {
	CookieName  string
	CookieValue string
	CSRFToken   string
	SessionID   string
	UserID      string
	Items       []webChatItem
}

func nexusDevLogin(client *httpJSONClient, email string) (nexusLogin, error) {
	body, _ := json.Marshal(map[string]string{"email": email})
	req, err := http.NewRequest(http.MethodPost, client.baseURL+"/webchat/dev/session", bytes.NewReader(body))
	if err != nil {
		return nexusLogin{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.client.Do(req)
	if err != nil {
		return nexusLogin{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nexusLogin{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return nexusLogin{}, fmt.Errorf("nexus dev login failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var payload struct {
		Data struct {
			CSRFToken string        `json:"csrf_token"`
			SessionID string        `json:"session_id"`
			UserID    string        `json:"user_id"`
			Items     []webChatItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nexusLogin{}, err
	}
	cookie := firstCookie(resp)
	if cookie == nil {
		return nexusLogin{}, errors.New("nexus dev login did not return session cookie")
	}
	return nexusLogin{
		CookieName:  cookie.Name,
		CookieValue: cookie.Value,
		CSRFToken:   payload.Data.CSRFToken,
		SessionID:   payload.Data.SessionID,
		UserID:      payload.Data.UserID,
		Items:       payload.Data.Items,
	}, nil
}

func nexusSendMessage(client *httpJSONClient, login nexusLogin, text string) error {
	var body bytes.Buffer
	writer := multipartWriter(&body, map[string]string{"text": text})
	req, err := http.NewRequest(http.MethodPost, client.baseURL+"/webchat/messages", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer)
	req.Header.Set("X-CSRF-Token", login.CSRFToken)
	req.AddCookie(&http.Cookie{Name: login.CookieName, Value: login.CookieValue})
	resp, err := client.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("nexus send message failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func waitForAssistantMessage(client *httpJSONClient, login nexusLogin, parmesan *httpJSONClient, parmesanSessionID string, seen map[string]struct{}, seenParmesan map[string]struct{}, contains []string, timeout time.Duration) (webChatItem, []webChatItem, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		items, err := nexusHistory(client, login)
		if err == nil {
			for _, item := range items {
				key := seenKey(item)
				if item.Role != "assistant" {
					seen[key] = struct{}{}
					continue
				}
				if _, ok := seen[key]; ok {
					continue
				}
				if containsAllFold(item.Text, contains) {
					seen[key] = struct{}{}
					return item, items, nil
				}
				if strings.TrimSpace(item.Text) != "" {
					seen[key] = struct{}{}
				}
			}
		}
		if parmesan != nil && strings.TrimSpace(parmesanSessionID) != "" {
			item, ok, err := latestParmesanAssistantMessage(parmesan, parmesanSessionID, seenParmesan, contains)
			if err == nil && ok {
				if items == nil {
					items, _ = nexusHistory(client, login)
				}
				return item, items, nil
			}
		}
		time.Sleep(1500 * time.Millisecond)
	}
	return webChatItem{}, nil, fmt.Errorf("timed out waiting for assistant message containing %v", contains)
}

func seenKey(item webChatItem) string {
	return item.ID + "\x00" + strings.TrimSpace(item.Text)
}

func latestParmesanAssistantMessage(client *httpJSONClient, parmesanSessionID string, seen map[string]struct{}, contains []string) (webChatItem, bool, error) {
	var events []struct {
		ID      string `json:"id"`
		Source  string `json:"source"`
		Kind    string `json:"kind"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := client.getJSON("/v1/operator/sessions/"+parmesanSessionID+"/events", &events); err != nil {
		return webChatItem{}, false, err
	}
	for _, evt := range events {
		if evt.Kind != "message" || evt.Source != "ai_agent" {
			continue
		}
		text := ""
		for _, part := range evt.Content {
			if strings.TrimSpace(part.Text) != "" {
				if text != "" {
					text += "\n\n"
				}
				text += strings.TrimSpace(part.Text)
			}
		}
		item := webChatItem{ID: evt.ID, Role: "assistant", Text: text}
		key := seenKey(item)
		if _, ok := seen[key]; ok {
			continue
		}
		if containsAllFold(item.Text, contains) {
			seen[key] = struct{}{}
			return item, true, nil
		}
		if strings.TrimSpace(item.Text) != "" {
			seen[key] = struct{}{}
		}
	}
	return webChatItem{}, false, nil
}

func nexusHistory(client *httpJSONClient, login nexusLogin) ([]webChatItem, error) {
	req, err := http.NewRequest(http.MethodGet, client.baseURL+"/webchat/history?limit=100", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-CSRF-Token", login.CSRFToken)
	req.AddCookie(&http.Cookie{Name: login.CookieName, Value: login.CookieValue})
	resp, err := client.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nexus history failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var payload struct {
		Data struct {
			Items []webChatItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload.Data.Items, nil
}

func lookupParmesanSessionID(client *httpJSONClient, gatewaySessionID string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var payload struct {
			Data struct {
				Session struct {
					ACPSessionID string `json:"ACPSessionID"`
				} `json:"session"`
			} `json:"data"`
		}
		if err := client.getJSON("/admin/sessions/detail?session_id="+gatewaySessionID+"&limit=100", &payload); err == nil {
			if value := strings.TrimSpace(payload.Data.Session.ACPSessionID); value != "" {
				return value, nil
			}
		}
		time.Sleep(1500 * time.Millisecond)
	}
	return "", fmt.Errorf("nexus session detail did not expose ACPSessionID for %s", gatewaySessionID)
}

func latestParmesanSessionID(client *httpJSONClient, agentID string) (string, error) {
	var sessions []struct {
		ID      string `json:"id"`
		AgentID string `json:"agent_id"`
	}
	if err := client.getJSON("/v1/operator/sessions", &sessions); err != nil {
		return "", err
	}
	for _, item := range sessions {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		if agentID == "" || strings.EqualFold(strings.TrimSpace(item.AgentID), strings.TrimSpace(agentID)) {
			return item.ID, nil
		}
	}
	return "", errors.New("no Parmesan operator session available")
}

func findDelegatedAgentResult(client *httpJSONClient, parmesanSessionID string) (map[string]any, error) {
	var events []struct {
		Kind string         `json:"kind"`
		Data map[string]any `json:"data"`
	}
	if err := client.getJSON("/v1/operator/sessions/"+parmesanSessionID+"/events", &events); err != nil {
		return nil, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == "agent.completed" {
			return events[i].Data, nil
		}
	}
	return nil, fmt.Errorf("no delegated agent completion event found for session %s", parmesanSessionID)
}

type lifecycleResponse struct {
	Watches []map[string]any `json:"watches"`
}

func getParmesanLifecycle(client *httpJSONClient, parmesanSessionID string) (lifecycleResponse, error) {
	var out lifecycleResponse
	err := client.getJSON("/v1/operator/sessions/"+parmesanSessionID+"/lifecycle", &out)
	return out, err
}

func getParmesanPreferences(client *httpJSONClient, agentID, customerID string) ([]map[string]any, error) {
	var out []map[string]any
	err := client.getJSON("/v1/operator/customers/"+customerID+"/preferences?agent_id="+agentID+"&limit=100", &out)
	return out, err
}

func callOrbyteMinimalResolve(client *httpJSONClient, ticketID string) (map[string]any, error) {
	return orbyteMCPCall(client, "tools/call", map[string]any{
		"name": "crm.ticket.resolve",
		"arguments": map[string]any{
			"ticket_id":        ticketID,
			"resolution_notes": "Resolved by live integration validation.",
			"confirm_apply":    true,
			"close":            false,
		},
	})
}

func callOrbyteLeadSearch(client *httpJSONClient, customerName string) (map[string]any, error) {
	return orbyteMCPCall(client, "tools/call", map[string]any{
		"name": "crm.lead.search",
		"arguments": map[string]any{
			"query":     customerName,
			"page_size": 20,
		},
	})
}

func orbyteMCPCall(client *httpJSONClient, method string, params map[string]any) (map[string]any, error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, err := http.NewRequest(http.MethodPost, client.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("orbyte mcp call failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var payload struct {
		Error  map[string]any `json:"error"`
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if payload.Error != nil {
		return nil, fmt.Errorf("orbyte mcp error: %#v", payload.Error)
	}
	return payload.Result, nil
}

func loadCRMManifest(path string) (crmManifest, error) {
	var out crmManifest
	raw, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

func loadScenarioManifest(path string) (scenarioManifest, error) {
	var out scenarioManifest
	raw, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

func loadScript(path string, values map[string]string) (scriptTemplate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return scriptTemplate{}, err
	}
	tmpl, err := template.New("validation").Parse(string(raw))
	if err != nil {
		return scriptTemplate{}, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, values); err != nil {
		return scriptTemplate{}, err
	}
	var out scriptTemplate
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		return scriptTemplate{}, err
	}
	return out, nil
}

func chooseProductName(manifest scenarioManifest) string {
	for _, key := range []string{"espresso", "croissant", "beans"} {
		if entity := manifest.Entities[key]; entity != nil {
			if name := strings.TrimSpace(stringValue(entity["name"])); name != "" {
				return name
			}
		}
	}
	for _, entity := range manifest.Entities {
		if name := strings.TrimSpace(stringValue(entity["name"])); name != "" {
			return name
		}
	}
	return ""
}

func hasPreference(items []map[string]any, key, value string) bool {
	for _, item := range items {
		if strings.EqualFold(stringValue(item["key"]), key) && strings.EqualFold(stringValue(item["value"]), value) {
			return true
		}
	}
	return false
}

func stringValue(v any) string {
	return strings.TrimSpace(fmt.Sprint(v))
}

func nestedInt(values map[string]any, key string) int {
	raw := values[key]
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	if structured, _ := values["structuredContent"].(map[string]any); structured != nil {
		switch v := structured[key].(type) {
		case float64:
			return int(v)
		case int:
			return v
		}
	}
	return 0
}

func containsAllFold(text string, needles []string) bool {
	lower := strings.ToLower(text)
	for _, needle := range needles {
		if strings.TrimSpace(needle) == "" {
			continue
		}
		if !strings.Contains(lower, strings.ToLower(strings.TrimSpace(needle))) {
			return false
		}
	}
	return true
}

func firstCookie(resp *http.Response) *http.Cookie {
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		return nil
	}
	return cookies[0]
}

func multipartWriter(body *bytes.Buffer, fields map[string]string) string {
	boundary := "boundary-orbyte-nexus-validation"
	for key, value := range fields {
		fmt.Fprintf(body, "--%s\r\n", boundary)
		fmt.Fprintf(body, "Content-Disposition: form-data; name=%q\r\n\r\n", key)
		fmt.Fprintf(body, "%s\r\n", value)
	}
	fmt.Fprintf(body, "--%s--\r\n", boundary)
	return "multipart/form-data; boundary=" + boundary
}

func ensureProviderCatalog(client *httpJSONClient, providers []providerSpec, requiredTools []string, timeout time.Duration) error {
	for _, provider := range providers {
		if err := registerProvider(client, provider); err != nil {
			return err
		}
		if err := syncProvider(client, provider.ID); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		names, err := listCatalogToolNames(client)
		if err == nil && containsAll(names, requiredTools) {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	_, err := listCatalogToolNames(client)
	if err != nil {
		return fmt.Errorf("wait for synced provider catalog: %w", err)
	}
	return fmt.Errorf("wait for synced provider catalog timed out; missing one or more tools from %v", requiredTools)
}

func registerProvider(client *httpJSONClient, provider providerSpec) error {
	body, _ := json.Marshal(provider)
	req, err := http.NewRequest(http.MethodPost, client.baseURL+"/v1/tools/providers/register", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range client.headers {
		req.Header.Set(key, value)
	}
	resp, err := client.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("register provider %s failed: status=%d body=%s", provider.ID, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func syncProvider(client *httpJSONClient, providerID string) error {
	req, err := http.NewRequest(http.MethodPost, client.baseURL+"/v1/tools/providers/"+providerID+"/sync", nil)
	if err != nil {
		return err
	}
	for key, value := range client.headers {
		req.Header.Set(key, value)
	}
	resp, err := client.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("sync provider %s failed: status=%d body=%s", providerID, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func listCatalogToolNames(client *httpJSONClient) ([]string, error) {
	var items []map[string]any
	if err := client.getJSON("/v1/tools/catalog", &items); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(stringValue(item["name"]))
		if name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

func containsAll(have []string, want []string) bool {
	seen := make(map[string]struct{}, len(have))
	for _, item := range have {
		seen[strings.TrimSpace(item)] = struct{}{}
	}
	for _, item := range want {
		if _, ok := seen[strings.TrimSpace(item)]; !ok {
			return false
		}
	}
	return true
}

func (c *httpJSONClient) getJSON(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s failed: status=%d body=%s", path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}
