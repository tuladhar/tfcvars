// tfcvars — sync Terraform Cloud / HCP Terraform workspace variables via the API.
//
// Requires -config-file (or -f) pointing at YAML: org, workspaces[].name, variables.
// Creates each variable when missing (same key + category); otherwise skips.
//
// Prefer setting "token" in YAML; if empty, uses TFC_TOKEN or TF_TOKEN.
// Colors: disabled when NO_COLOR is set or TERM=dumb.
//
//	go run . -f config.yaml
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const cliName = "tfcvars"

// --- CLI output -------------------------------------------------------------

type cliUI struct {
	color bool
}

func newCLIUI() *cliUI {
	return &cliUI{color: os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"}
}

func (u *cliUI) s(code, text string) string {
	if !u.color {
		return text
	}
	return code + text + "\033[0m"
}

func (u *cliUI) bold(s string) string { return u.s("\033[1m", s) }
func (u *cliUI) green(s string) string {
	return u.s("\033[32m", s)
}

func (u *cliUI) dim(s string) string { return u.s("\033[90m", s) }

func (u *cliUI) printHeader(cfg *fileConfig, configPath string) {
	abs, _ := filepath.Abs(configPath)
	fmt.Println(u.bold(cliName + " — Terraform Cloud workspace variables"))
	fmt.Println()
	fmt.Printf("  %s %q\n", u.dim("Config"), abs)
	fmt.Printf("  %s %s\n", u.dim("API host"), cfg.Hostname)
	fmt.Printf("  %s %q\n", u.dim("Organization"), cfg.Org)
	fmt.Printf("  %s %d\n", u.dim("Workspaces"), len(cfg.Workspaces))
	fmt.Println()
	fmt.Printf("%s  %s created    %s skipped (already exists)\n",
		u.dim("Legend:"), u.green("+"), u.dim("·"))
	fmt.Println()
	u.separator()
}

func (u *cliUI) separator() {
	fmt.Println(u.dim("──────────────────────────────────────────────────────────────────────────────"))
	fmt.Println()
}

type runTotals struct {
	created int
	skipped int
}

func (u *cliUI) printWorkspace(org, workspaceName string) {
	fmt.Printf("%s  organization %s  workspace %s\n",
		u.bold("Scope"), strconv.Quote(org), strconv.Quote(workspaceName))
	fmt.Println()
}

func (u *cliUI) printVarCreated(key, category, value, description, id string, sensitive, hcl bool) {
	fmt.Println(u.green("  +") + " " + key)
	fmt.Printf("      %-12s %s\n", u.dim("category"), category)
	if sensitive {
		fmt.Printf("      %-12s %s\n", u.dim("value"), "(not shown)")
	} else {
		fmt.Printf("      %-12s %s\n", u.dim("value"), value)
	}
	fmt.Printf("      %-12s %t\n", u.dim("sensitive"), sensitive)
	fmt.Printf("      %-12s %t\n", u.dim("hcl"), hcl)
	if description != "" {
		fmt.Printf("      %-12s %s\n", u.dim("description"), description)
	}
	if id != "" && id != "?" {
		fmt.Printf("      %-12s %s\n", u.dim("id"), id)
	}
	fmt.Println()
}

func (u *cliUI) printVarSkipped(key, category, detail string) {
	msg := fmt.Sprintf("  · %s  category=%s  skipped", key, category)
	if detail != "" {
		msg += " (" + detail + ")"
	}
	fmt.Println(u.dim(msg))
	fmt.Println()
}

func (u *cliUI) printSummary(totals runTotals) {
	u.separator()
	fmt.Println(u.bold("Summary"))
	fmt.Printf("  %-10s %d\n", u.dim("Created"), totals.created)
	fmt.Printf("  %-10s %d\n", u.dim("Skipped"), totals.skipped)
	fmt.Println()
	if u.color {
		fmt.Println(u.green("Finished successfully."))
	} else {
		fmt.Println("Finished successfully.")
	}
	fmt.Println()
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func apiRequest(client *http.Client, method, u, token string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/vnd.api+json")
	return client.Do(req)
}

func readJSON(resp *http.Response, out any) error {
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var pretty any
		if json.Unmarshal(data, &pretty) == nil {
			enc, _ := json.MarshalIndent(pretty, "", "  ")
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, enc)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(data))
	}
	return json.Unmarshal(data, out)
}

type workspaceShow struct {
	Data *struct {
		ID string `json:"id"`
	} `json:"data"`
}

type varResource struct {
	ID         string `json:"id"`
	Attributes struct {
		Key      string `json:"key"`
		Category string `json:"category"`
	} `json:"attributes"`
}

type varsListResponse struct {
	Data []varResource `json:"data"`
}

func listAllWorkspaceVars(client *http.Client, base, wsID, token string) ([]varResource, error) {
	const pageSize = 100
	var all []varResource
	for pageNum := 1; ; pageNum++ {
		u := fmt.Sprintf("%s/api/v2/workspaces/%s/vars?page[number]=%d&page[size]=%d",
			base, wsID, pageNum, pageSize)
		resp, err := apiRequest(client, http.MethodGet, u, token, nil)
		if err != nil {
			return nil, err
		}
		var page varsListResponse
		if err := readJSON(resp, &page); err != nil {
			return nil, err
		}
		if len(page.Data) == 0 {
			break
		}
		all = append(all, page.Data...)
		if len(page.Data) < pageSize {
			break
		}
	}
	return all, nil
}

func findVar(existing []varResource, key, category string) *varResource {
	for i := range existing {
		e := &existing[i]
		if e.Attributes.Key == key && e.Attributes.Category == category {
			return e
		}
	}
	return nil
}

// --- YAML config ---

type fileConfig struct {
	Token      string               `yaml:"token"`
	Hostname   string               `yaml:"hostname"`
	Org        string               `yaml:"org"`
	Workspace  string               `yaml:"workspace"`  // legacy: single workspace name
	Variables  []fileConfigVarItem  `yaml:"variables"`  // legacy: vars for workspace above
	Workspaces []fileConfigWorkspace `yaml:"workspaces"`
}

type fileConfigWorkspace struct {
	Name      string              `yaml:"name"`
	Org       string              `yaml:"org"` // optional; defaults to top-level org
	Variables []fileConfigVarItem `yaml:"variables"`
}

type fileConfigVarItem struct {
	Key         string `yaml:"key"`
	Value       string `yaml:"value"`
	Category    string `yaml:"category"`
	Sensitive   *bool  `yaml:"sensitive"`
	HCL         *bool  `yaml:"hcl"`
	Description string `yaml:"description"`
}

func loadConfig(path string) (*fileConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg fileConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	if cfg.Hostname == "" {
		cfg.Hostname = envOrDefault("TFC_ADDRESS", "https://app.terraform.io")
	}
	if cfg.Org == "" {
		cfg.Org = envOrDefault("TFC_ORG", "covergo")
	}
	// Prefer workspaces[]; migrate legacy single workspace + top-level variables.
	if len(cfg.Workspaces) == 0 {
		if cfg.Workspace != "" || len(cfg.Variables) > 0 {
			wsName := cfg.Workspace
			if wsName == "" {
				wsName = envOrDefault("TFC_WORKSPACE", "aws-covergo-cloud-ap-southeast-1-dev")
			}
			cfg.Workspaces = []fileConfigWorkspace{{Name: wsName, Variables: cfg.Variables}}
		}
	}
	for wi := range cfg.Workspaces {
		ws := &cfg.Workspaces[wi]
		for vi := range ws.Variables {
			normalizeVarItem(&ws.Variables[vi])
		}
	}
	return &cfg, nil
}

func normalizeVarItem(v *fileConfigVarItem) {
	if v.Category == "" {
		v.Category = "terraform"
	}
	if v.Sensitive == nil {
		def := true
		v.Sensitive = &def
	}
	if v.HCL == nil {
		def := false
		v.HCL = &def
	}
}

func validateCategory(cat string) error {
	if cat != "terraform" && cat != "env" {
		return fmt.Errorf("category must be terraform or env, got %q", cat)
	}
	return nil
}

func runFromConfig(client *http.Client, base string, cfg *fileConfig, token string, ui *cliUI) (runTotals, error) {
	var totals runTotals
	if len(cfg.Workspaces) == 0 {
		return totals, errors.New("config must define workspaces with at least one entry (see config.example.yaml)")
	}
	for _, ws := range cfg.Workspaces {
		if strings.TrimSpace(ws.Name) == "" {
			return totals, errors.New("workspaces[].name is required")
		}
		if len(ws.Variables) == 0 {
			return totals, fmt.Errorf("workspace %q: variables[] is empty", ws.Name)
		}
		org := cfg.Org
		if strings.TrimSpace(ws.Org) != "" {
			org = strings.TrimSpace(ws.Org)
		}
		if org == "" {
			return totals, fmt.Errorf("workspace %q: organization is empty (set top-level org or workspaces[].org)", ws.Name)
		}
		ui.printWorkspace(org, ws.Name)
		cr, sk, err := syncWorkspaceVariables(client, base, org, ws.Name, token, ws.Variables, ui)
		totals.created += cr
		totals.skipped += sk
		if err != nil {
			return totals, fmt.Errorf("workspace %q: %w", ws.Name, err)
		}
	}
	return totals, nil
}

func syncWorkspaceVariables(client *http.Client, base, org, workspaceName, token string, items []fileConfigVarItem, ui *cliUI) (created, skipped int, err error) {
	showURL := fmt.Sprintf("%s/api/v2/organizations/%s/workspaces/%s",
		base,
		url.PathEscape(org),
		url.PathEscape(workspaceName),
	)
	resp, err := apiRequest(client, http.MethodGet, showURL, token, nil)
	if err != nil {
		return 0, 0, err
	}
	var ws workspaceShow
	if err := readJSON(resp, &ws); err != nil {
		return 0, 0, err
	}
	if ws.Data == nil || ws.Data.ID == "" {
		return 0, 0, errors.New("could not read workspace id from API response")
	}
	wsID := ws.Data.ID

	existing, err := listAllWorkspaceVars(client, base, wsID, token)
	if err != nil {
		return 0, 0, err
	}

	createURL := fmt.Sprintf("%s/api/v2/workspaces/%s/vars", base, wsID)
	for _, item := range items {
		if item.Key == "" {
			return created, skipped, errors.New("variables[].key is required")
		}
		if err := validateCategory(item.Category); err != nil {
			return created, skipped, fmt.Errorf("variable %q: %w", item.Key, err)
		}
		if findVar(existing, item.Key, item.Category) != nil {
			ui.printVarSkipped(item.Key, item.Category, "")
			skipped++
			continue
		}
		attrs := map[string]any{
			"key":       item.Key,
			"value":     item.Value,
			"category":  item.Category,
			"hcl":       *item.HCL,
			"sensitive": *item.Sensitive,
		}
		if item.Description != "" {
			attrs["description"] = item.Description
		}
		payload := map[string]any{
			"data": map[string]any{
				"type":       "vars",
				"attributes": attrs,
			},
		}
		resp, err := apiRequest(client, http.MethodPost, createURL, token, payload)
		if err != nil {
			return created, skipped, err
		}
		var createdBody struct {
			Data *struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := readJSON(resp, &createdBody); err != nil {
			if strings.Contains(err.Error(), "422") {
				ui.printVarSkipped(item.Key, item.Category, "already exists (concurrent create)")
				skipped++
				continue
			}
			return created, skipped, err
		}
		varID := "?"
		if createdBody.Data != nil && createdBody.Data.ID != "" {
			varID = createdBody.Data.ID
		}
		ui.printVarCreated(item.Key, item.Category, item.Value, item.Description, varID, *item.Sensitive, *item.HCL)
		created++
		existing = append(existing, varResource{
			Attributes: struct {
				Key      string `json:"key"`
				Category string `json:"category"`
			}{Key: item.Key, Category: item.Category},
		})
	}
	return created, skipped, nil
}

func tokenFromEnv() (string, error) {
	t := os.Getenv("TFC_TOKEN")
	if t == "" {
		t = os.Getenv("TF_TOKEN")
	}
	if t == "" {
		return "", errors.New("set TFC_TOKEN or TF_TOKEN to a Terraform Cloud API token")
	}
	return t, nil
}

// tokenForConfig returns cfg.token when set; otherwise TFC_TOKEN / TF_TOKEN.
func tokenForConfig(cfg *fileConfig) (string, error) {
	if strings.TrimSpace(cfg.Token) != "" {
		return strings.TrimSpace(cfg.Token), nil
	}
	return tokenFromEnv()
}

func main() {
	var configPath string
	flag.StringVar(&configPath, "config-file", "", "path to YAML config (required)")
	flag.StringVar(&configPath, "f", "", "path to YAML config (shorthand)")
	flag.Parse()

	if strings.TrimSpace(configPath) == "" {
		fmt.Fprintf(os.Stderr, "%s: missing config file\n\n", cliName)
		fmt.Fprintf(os.Stderr, "Usage:  %s -config-file <config.yaml>\n        %s -f <config.yaml>\n\n", cliName, cliName)
		fmt.Fprintf(os.Stderr, "Manage Terraform Cloud / HCP Terraform workspace variables from YAML.\n")
		fmt.Fprintf(os.Stderr, "See README.md in this directory.\n")
		os.Exit(2)
	}
	configPath = strings.TrimSpace(configPath)
	if _, err := os.Stat(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "config file not found: %s\n", configPath)
		os.Exit(1)
	}

	client := &http.Client{}
	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config %s: %v\n", configPath, err)
		os.Exit(1)
	}
	if len(cfg.Workspaces) == 0 {
		fmt.Fprintln(os.Stderr, "config must define workspaces with at least one entry")
		os.Exit(1)
	}
	token, err := tokenForConfig(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ui := newCLIUI()
	ui.printHeader(cfg, configPath)
	base := strings.TrimRight(cfg.Hostname, "/")
	totals, err := runFromConfig(client, base, cfg, token, ui)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ui.printSummary(totals)
}
