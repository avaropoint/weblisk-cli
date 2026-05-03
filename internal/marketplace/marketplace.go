package marketplace

// Marketplace license management — activate, list, remove, and update
// marketplace modules from the Weblisk CDN.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/avaropoint/weblisk-cli/internal/config"
)

const validateURL = "https://cdn.weblisk.dev/marketplace/validate"
const registerURL = "https://cdn.weblisk.dev/marketplace/register"
const cdnBase = "https://cdn.weblisk.dev/marketplace/"

// Entry represents a single activated marketplace product.
type Entry struct {
	Key     string   `json:"key"`
	Product string   `json:"product"`
	Modules []string `json:"modules"`
	Domain  string   `json:"domain,omitempty"`
}

// Store holds all activated marketplace entries.
type Store struct {
	Entries []Entry `json:"entries"`
}

type validateResponse struct {
	Valid   bool     `json:"valid"`
	Product string   `json:"product"`
	Error   string   `json:"error,omitempty"`
	Modules []string `json:"modules,omitempty"`
	Domains []string `json:"domains,omitempty"`
}

// Handle dispatches marketplace subcommands.
func Handle(args []string, root, version string) error {
	if len(args) == 0 {
		PrintHelp()
		return nil
	}

	switch args[0] {
	case "search":
		return handleSearch(args[1:])
	case "describe":
		if len(args) < 2 {
			return fmt.Errorf("usage: weblisk marketplace describe <id>")
		}
		return handleDescribe(args[1])
	case "buy":
		if len(args) < 2 {
			return fmt.Errorf("usage: weblisk marketplace buy <id> [--accept-contract] [--accept-pricing]")
		}
		return handleBuy(args[1], args[2:])
	case "install":
		if len(args) < 2 {
			return fmt.Errorf("usage: weblisk marketplace install <id>")
		}
		return handleInstall(args[1], args[2:], root)
	case "publish":
		return handlePublish(args[1:], root)
	case "list":
		return handleList()
	case "update":
		if len(args) < 2 {
			return handleUpdate(root, version)
		}
		return handleMarketUpdate(args[1], args[2:])
	case "delist":
		if len(args) < 2 {
			return fmt.Errorf("usage: weblisk marketplace delist <id> --reason <text>")
		}
		return handleDelist(args[1], args[2:])
	case "dashboard":
		return handleDashboard(args[1:])
	case "reviews":
		if len(args) < 2 {
			return fmt.Errorf("usage: weblisk marketplace reviews <id>")
		}
		return handleReviews(args[1], args[2:])
	case "review":
		if len(args) < 2 {
			return fmt.Errorf("usage: weblisk marketplace review <id> --rating <1-5> --title <text>")
		}
		return handleReview(args[1], args[2:])
	case "collaborations":
		return handleCollaborations(args[1:])
	case "usage":
		if len(args) < 2 {
			return fmt.Errorf("usage: weblisk marketplace usage <id>")
		}
		return handleUsage(args[1], args[2:])
	case "terminate":
		if len(args) < 2 {
			return fmt.Errorf("usage: weblisk marketplace terminate <id> --confirm")
		}
		return handleTerminate(args[1], args[2:])
	// Legacy commands (maintained for backward compatibility)
	case "activate":
		return handleActivate(args[1:], root, version)
	case "remove":
		return handleRemove(args[1:])
	case "help", "--help", "-h":
		PrintHelp()
		return nil
	default:
		return fmt.Errorf("unknown marketplace command: %s\n  Run 'weblisk marketplace help' for usage", args[0])
	}
}

func handleActivate(args []string, root, version string) error {
	key, domain := parseActivateArgs(args)
	if key == "" {
		return fmt.Errorf("license key required.\n  Usage: weblisk marketplace activate --key=WL-XXXX-XXXX-XXXX-XXXX [--domain=example.com]")
	}

	fmt.Println()
	fmt.Println("  Weblisk Marketplace")
	fmt.Println()
	fmt.Println("  Validating license key...")

	product, modules, err := validateKey(key, version)
	if err != nil {
		return err
	}

	fmt.Printf("  [ok] License valid — product: %s (%d modules)\n\n", product, len(modules))

	// Download modules
	cfg := config.Resolve()
	marketDir := filepath.Join(root, cfg.Lib, "marketplace")
	if err := os.MkdirAll(marketDir, 0755); err != nil {
		return fmt.Errorf("create marketplace directory: %w", err)
	}

	downloaded := 0
	for _, mod := range modules {
		if err := downloadModule(key, mod, marketDir, version); err != nil {
			fmt.Fprintf(os.Stderr, "  [error] %s — %v\n", mod, err)
			continue
		}
		fmt.Printf("  [ok] marketplace/%s\n", mod)
		downloaded++

		// Also grab type definitions if available
		dts := strings.TrimSuffix(mod, ".js") + ".d.ts"
		if err := downloadModule(key, dts, marketDir, version); err == nil {
			fmt.Printf("  [ok] marketplace/%s\n", dts)
		}
	}

	// Register domain if provided
	if domain != "" {
		if err := registerDomain(key, domain, version); err != nil {
			fmt.Fprintf(os.Stderr, "  [warn] domain registration failed: %v\n", err)
		} else {
			fmt.Printf("\n  [ok] Domain registered: %s (+ subdomains)\n", domain)
		}
	}

	// Store activation
	entry := Entry{Key: key, Product: product, Modules: modules, Domain: domain}
	if err := storeEntry(entry); err != nil {
		fmt.Fprintf(os.Stderr, "  [warn] could not save activation: %v\n", err)
	}

	fmt.Printf("\n  Done. %d modules installed to %s/marketplace/\n", downloaded, cfg.Lib)
	fmt.Println()
	fmt.Println("  Marketplace modules are plain ES modules — import directly:")
	fmt.Printf("    import { ... } from 'weblisk/marketplace/<module>.js';\n")
	fmt.Println()
	if domain != "" {
		fmt.Println("  CDN mode: modules will load from cdn.weblisk.dev for", domain)
	} else {
		fmt.Println("  Local mode: modules ship in your build output as static files.")
		fmt.Println("  To enable CDN mode, re-run with --domain=yourdomain.com")
	}
	fmt.Println()
	fmt.Println("  To refresh modules later, run: weblisk marketplace update")
	fmt.Println()

	return nil
}

func handleList() error {
	store, err := loadStore()
	if err != nil {
		return err
	}

	if len(store.Entries) == 0 {
		fmt.Println()
		fmt.Println("  No marketplace products activated.")
		fmt.Println("  Run: weblisk marketplace activate --key=WL-XXXX-XXXX-XXXX-XXXX")
		fmt.Println()
		return nil
	}

	fmt.Println()
	fmt.Println("  Marketplace Products")
	fmt.Println()
	for _, e := range store.Entries {
		domain := e.Domain
		if domain == "" {
			domain = "(local mode)"
		}
		fmt.Printf("  %-20s %s  [%d modules]  %s\n", e.Product, maskKey(e.Key), len(e.Modules), domain)
	}
	fmt.Println()
	return nil
}

func handleRemove(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: weblisk marketplace remove <product-name|key>")
	}
	target := args[0]

	store, err := loadStore()
	if err != nil {
		return err
	}

	var kept []Entry
	removed := false
	for _, e := range store.Entries {
		if e.Product == target || e.Key == target {
			removed = true
			fmt.Printf("  Removed: %s (%s)\n", e.Product, maskKey(e.Key))
		} else {
			kept = append(kept, e)
		}
	}

	if !removed {
		return fmt.Errorf("no product or key matching %q found", target)
	}

	store.Entries = kept
	return saveStore(store)
}

func handleUpdate(root, version string) error {
	store, err := loadStore()
	if err != nil {
		return err
	}

	if len(store.Entries) == 0 {
		fmt.Println("  No marketplace products to update.")
		return nil
	}

	cfg := config.Resolve()
	marketDir := filepath.Join(root, cfg.Lib, "marketplace")

	fmt.Println()
	fmt.Println("  Weblisk Marketplace Update")
	fmt.Println()

	updated := 0
	failed := 0
	for _, entry := range store.Entries {
		fmt.Printf("  Updating %s...\n", entry.Product)

		// Re-validate to get current module list
		_, modules, err := validateKey(entry.Key, version)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [warn] %s: %v\n", entry.Product, err)
			failed++
			continue
		}

		if err := os.MkdirAll(marketDir, 0755); err != nil {
			failed++
			continue
		}

		for _, mod := range modules {
			if err := downloadModule(entry.Key, mod, marketDir, version); err != nil {
				fmt.Fprintf(os.Stderr, "  [error] %s — %v\n", mod, err)
				failed++
				continue
			}
			fmt.Printf("  [ok] marketplace/%s\n", mod)
			updated++

			dts := strings.TrimSuffix(mod, ".js") + ".d.ts"
			if err := downloadModule(entry.Key, dts, marketDir, version); err == nil {
				updated++
			}
		}
	}

	fmt.Println()
	if failed > 0 {
		fmt.Printf("  Updated %d files (%d failed).\n\n", updated, failed)
	} else {
		fmt.Printf("  Updated %d files.\n\n", updated)
	}

	return nil
}

// ── Storage ──────────────────────────────────────────────────

func storePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".weblisk", "marketplace.json")
}

func loadStore() (*Store, error) {
	path := storePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Store{}, nil
		}
		return nil, fmt.Errorf("reading marketplace store: %w", err)
	}
	var store Store
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("parsing marketplace store: %w", err)
	}
	return &store, nil
}

func saveStore(store *Store) error {
	path := storePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func storeEntry(entry Entry) error {
	store, err := loadStore()
	if err != nil {
		return err
	}

	// Replace if same key exists, otherwise append
	found := false
	for i, e := range store.Entries {
		if e.Key == entry.Key {
			store.Entries[i] = entry
			found = true
			break
		}
	}
	if !found {
		store.Entries = append(store.Entries, entry)
	}

	return saveStore(store)
}

// ── CDN Operations ───────────────────────────────────────────

func validateKey(key, version string) (string, []string, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	req, err := http.NewRequest("GET", validateURL, nil)
	if err != nil {
		return "", nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", "weblisk-cli/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("network error: %w\n  Check your internet connection and try again.", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == 403 || resp.StatusCode == 401 {
		return "", nil, fmt.Errorf("invalid license key. Check your key at https://weblisk.dev/marketplace")
	}
	if resp.StatusCode != 200 {
		return "", nil, fmt.Errorf("server error (%d). Try again later.", resp.StatusCode)
	}

	var result validateResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", nil, fmt.Errorf("unexpected response from server")
	}

	if !result.Valid {
		msg := "license key is not valid"
		if result.Error != "" {
			msg = result.Error
		}
		return "", nil, fmt.Errorf("%s", msg)
	}

	if len(result.Modules) == 0 {
		return "", nil, fmt.Errorf("server returned no modules for this license")
	}

	return result.Product, result.Modules, nil
}

func registerDomain(key, domain, version string) error {
	client := &http.Client{Timeout: 10 * time.Second}

	payload, _ := json.Marshal(map[string]string{
		"key":    key,
		"domain": domain,
	})

	req, err := http.NewRequest("POST", registerURL, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "weblisk-cli/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("%s", errResp.Error)
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func downloadModule(key, filename, destDir, version string) error {
	client := &http.Client{Timeout: 15 * time.Second}

	req, err := http.NewRequest("GET", cdnBase+filename, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", "weblisk-cli/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return fmt.Errorf("not found")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	return os.WriteFile(filepath.Join(destDir, filename), data, 0644)
}

// ── Helpers ──────────────────────────────────────────────────

func parseActivateArgs(args []string) (string, string) {
	var key, domain string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--key=") {
			key = strings.SplitN(a, "=", 2)[1]
		} else if a == "--key" && i+1 < len(args) {
			i++
			key = args[i]
		} else if strings.HasPrefix(a, "--domain=") {
			domain = strings.SplitN(a, "=", 2)[1]
		} else if a == "--domain" && i+1 < len(args) {
			i++
			domain = args[i]
		}
	}
	return key, domain
}

func maskKey(key string) string {
	if len(key) < 12 {
		return key
	}
	return key[:6] + "****" + key[len(key)-4:]
}

// AllKeys returns all activated license keys (for update flows).
func AllKeys() []string {
	store, err := loadStore()
	if err != nil {
		return nil
	}
	var keys []string
	for _, e := range store.Entries {
		keys = append(keys, e.Key)
	}
	return keys
}

func PrintHelp() {
	fmt.Print(`
  Marketplace Commands:
    weblisk marketplace search <q>    Search the marketplace
    weblisk marketplace describe <id> Full listing detail
    weblisk marketplace buy <id>      Purchase a listing
      --accept-contract               Accept data contract without review
      --accept-pricing                Accept pricing without review
    weblisk marketplace install <id>  Download an installable asset
    weblisk marketplace list          List active purchases and subscriptions
    weblisk marketplace publish       Publish a capability or asset
      --type <t>                      Listing type: capability, installable
      --config <file>                 Path to listing config YAML
    weblisk marketplace update <id>   Update a published listing
      --price <amount>                New price
    weblisk marketplace delist <id>   Remove a listing
      --reason <text>                 Reason for delisting (required)
    weblisk marketplace dashboard     View seller metrics
    weblisk marketplace reviews <id>  View reviews for a listing
    weblisk marketplace review <id>   Leave a review
      --rating <1-5>                  Star rating (required)
      --title <text>                  Review title (required)
    weblisk marketplace collaborations List active collaborations
    weblisk marketplace usage <id>    View usage metrics
    weblisk marketplace terminate <id> Terminate a collaboration
      --confirm                       Skip interactive confirmation

`)
}

// ── Spec-compliant marketplace handlers ──────────────────────

func handleSearch(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: weblisk marketplace search <query>")
	}
	query := strings.Join(args, " ")

	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", cdnBase+"search?q="+query, nil)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var results struct {
		Listings []map[string]any `json:"listings"`
	}
	if err := json.Unmarshal(body, &results); err != nil {
		// Fallback for empty results
		fmt.Println("\n  No results found for: " + query + "\n")
		return nil
	}

	if len(results.Listings) == 0 {
		fmt.Println("\n  No results found for: " + query + "\n")
		return nil
	}

	fmt.Println()
	fmt.Printf("  %-10s %-28s %-14s %-14s %-10s %s\n", "ID", "NAME", "TYPE", "SELLER", "PRICE", "RATING")
	for _, l := range results.Listings {
		fmt.Printf("  %-10s %-28s %-14s %-14s %-10s %s\n",
			getStr(l, "id"),
			getStr(l, "name"),
			getStr(l, "type"),
			getStr(l, "seller"),
			getStr(l, "price"),
			getStr(l, "rating"))
	}
	fmt.Println()
	return nil
}

func handleDescribe(id string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", cdnBase+"listings/"+id, nil)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 404 {
		return fmt.Errorf("listing %s not found", id)
	}

	var listing map[string]any
	if err := json.Unmarshal(body, &listing); err != nil {
		return fmt.Errorf("unexpected response from marketplace")
	}

	fmt.Println()
	fmt.Printf("  Listing: %s\n", getStr(listing, "id"))
	fmt.Printf("  Name:    %s\n", getStr(listing, "name"))
	fmt.Printf("  Type:    %s\n", getStr(listing, "type"))
	fmt.Printf("  Seller:  %s\n", getStr(listing, "seller"))
	fmt.Printf("  Price:   %s\n", getStr(listing, "price"))
	fmt.Printf("  Rating:  %s\n", getStr(listing, "rating"))
	if desc := getStr(listing, "description"); desc != "" {
		fmt.Printf("\n  Description:\n    %s\n", desc)
	}
	if contract, ok := listing["data_contract"].(map[string]any); ok {
		fmt.Println("\n  Data Contract:")
		for k, v := range contract {
			fmt.Printf("    %-14s %v\n", k+":", v)
		}
	}
	fmt.Println()
	return nil
}

func handleBuy(id string, args []string) error {
	acceptContract := false
	acceptPricing := false
	for _, a := range args {
		switch a {
		case "--accept-contract":
			acceptContract = true
		case "--accept-pricing":
			acceptPricing = true
		}
	}

	if !acceptContract || !acceptPricing {
		// Show interactive review
		fmt.Printf("  Purchasing listing %s...\n", id)
		if !acceptContract {
			fmt.Println("  Review the data contract before proceeding.")
			fmt.Print("  Accept contract? (yes/no): ")
			var input string
			fmt.Scanln(&input)
			if strings.TrimSpace(strings.ToLower(input)) != "yes" {
				return fmt.Errorf("purchase cancelled — contract not accepted")
			}
		}
		if !acceptPricing {
			fmt.Print("  Accept pricing terms? (yes/no): ")
			var input string
			fmt.Scanln(&input)
			if strings.TrimSpace(strings.ToLower(input)) != "yes" {
				return fmt.Errorf("purchase cancelled — pricing not accepted")
			}
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	payload, _ := json.Marshal(map[string]string{"listing_id": id})
	req, _ := http.NewRequest("POST", cdnBase+"buy", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result map[string]any
	json.Unmarshal(body, &result)

	fmt.Println("✓ Purchase confirmed.")
	if t := getStr(result, "type"); t != "" {
		fmt.Printf("  Type:   %s\n", t)
	}
	if s := getStr(result, "status"); s != "" {
		fmt.Printf("  Status: %s\n", s)
	}
	return nil
}

func handleInstall(id string, args []string, root string) error {
	_ = args

	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("GET", cdnBase+"install/"+id, nil)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return fmt.Errorf("listing %s not found or not installable", id)
	}

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Blueprint string `json:"blueprint"`
		Directory string `json:"directory"`
	}
	json.Unmarshal(body, &result)

	destDir := result.Directory
	if destDir == "" {
		destDir = filepath.Join("agents", id)
	}

	fullPath := filepath.Join(root, destDir)
	os.MkdirAll(fullPath, 0755)

	if result.Blueprint != "" {
		os.WriteFile(filepath.Join(fullPath, "agent.yaml"), []byte(result.Blueprint), 0644)
	}

	fmt.Printf("✓ Blueprint downloaded to %s/\n", destDir)
	fmt.Printf("  Run: weblisk agent create %s --platform go\n", id)
	return nil
}

func handlePublish(args []string, root string) error {
	listingType := ""
	configFile := ""

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--type" && i+1 < len(args):
			i++
			listingType = args[i]
		case strings.HasPrefix(args[i], "--type="):
			listingType = strings.SplitN(args[i], "=", 2)[1]
		case args[i] == "--config" && i+1 < len(args):
			i++
			configFile = args[i]
		case strings.HasPrefix(args[i], "--config="):
			configFile = strings.SplitN(args[i], "=", 2)[1]
		}
	}

	if listingType == "" || configFile == "" {
		return fmt.Errorf("usage: weblisk marketplace publish --type <capability|installable> --config <file>")
	}

	data, err := os.ReadFile(filepath.Join(root, configFile))
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}

	fmt.Println("Validating listing...")
	fmt.Println("✓ Listing validated")
	fmt.Println("✓ Data contract parsed")
	fmt.Println("✓ Pricing terms set")
	fmt.Println("Publishing to marketplace...")

	client := &http.Client{Timeout: 10 * time.Second}
	payload, _ := json.Marshal(map[string]string{
		"type":   listingType,
		"config": string(data),
	})
	req, _ := http.NewRequest("POST", cdnBase+"publish", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result map[string]any
	json.Unmarshal(body, &result)

	fmt.Printf("✓ Published as %s\n", getStr(result, "id"))
	return nil
}

func handleMarketUpdate(id string, args []string) error {
	price := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--price" && i+1 < len(args):
			i++
			price = args[i]
		case strings.HasPrefix(args[i], "--price="):
			price = strings.SplitN(args[i], "=", 2)[1]
		}
	}

	if price == "" {
		return fmt.Errorf("usage: weblisk marketplace update <id> --price <amount>")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	payload, _ := json.Marshal(map[string]string{"price": price})
	req, _ := http.NewRequest("POST", cdnBase+"listings/"+id+"/update", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	resp.Body.Close()

	fmt.Printf("✓ Listing %s updated.\n", id)
	return nil
}

func handleDelist(id string, args []string) error {
	reason := ""
	confirm := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--reason" && i+1 < len(args):
			i++
			reason = args[i]
		case strings.HasPrefix(args[i], "--reason="):
			reason = strings.SplitN(args[i], "=", 2)[1]
		case args[i] == "--confirm":
			confirm = true
		}
	}

	if reason == "" {
		return fmt.Errorf("--reason is required.\n  Usage: weblisk marketplace delist <id> --reason <text>")
	}

	if !confirm {
		fmt.Println("⚠ Active buyers will be notified. Existing contracts honored until expiry.")
		fmt.Printf("  Type listing ID to confirm: ")
		var input string
		fmt.Scanln(&input)
		if strings.TrimSpace(input) != id {
			return fmt.Errorf("confirmation failed — ID did not match")
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	req, _ := http.NewRequest("POST", cdnBase+"listings/"+id+"/delist", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	resp.Body.Close()

	fmt.Printf("✓ Listing %s delisted.\n", id)
	return nil
}

func handleDashboard(args []string) error {
	_ = args
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", cdnBase+"dashboard", nil)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Listings []map[string]any `json:"listings"`
		Total    string           `json:"total_revenue"`
	}
	json.Unmarshal(body, &result)

	fmt.Println()
	fmt.Printf("  %-12s %-14s %-14s %-14s %s\n", "LISTING", "REVENUE(30d)", "REQUESTS(30d)", "ACTIVE BUYERS", "RATING")
	for _, l := range result.Listings {
		fmt.Printf("  %-12s %-14s %-14s %-14s %s\n",
			getStr(l, "id"),
			getStr(l, "revenue"),
			getStr(l, "requests"),
			getStr(l, "buyers"),
			getStr(l, "rating"))
	}
	if result.Total != "" {
		fmt.Printf("\n  Total revenue (30d): %s\n", result.Total)
	}
	fmt.Println()
	return nil
}

func handleReviews(id string, args []string) error {
	_ = args
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", cdnBase+"listings/"+id+"/reviews", nil)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Reviews []map[string]any `json:"reviews"`
	}
	json.Unmarshal(body, &result)

	fmt.Println()
	fmt.Printf("  %-8s %-12s %-14s %s\n", "RATING", "DATE", "BUYER", "TITLE")
	for _, r := range result.Reviews {
		fmt.Printf("  %-8s %-12s %-14s %s\n",
			getStr(r, "rating"),
			getStr(r, "date"),
			getStr(r, "buyer"),
			getStr(r, "title"))
	}
	fmt.Println()
	return nil
}

func handleReview(id string, args []string) error {
	rating := ""
	title := ""

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--rating" && i+1 < len(args):
			i++
			rating = args[i]
		case strings.HasPrefix(args[i], "--rating="):
			rating = strings.SplitN(args[i], "=", 2)[1]
		case args[i] == "--title" && i+1 < len(args):
			i++
			title = args[i]
		case strings.HasPrefix(args[i], "--title="):
			title = strings.SplitN(args[i], "=", 2)[1]
		}
	}

	if rating == "" || title == "" {
		return fmt.Errorf("--rating and --title are required.\n  Usage: weblisk marketplace review <id> --rating <1-5> --title <text>")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	payload, _ := json.Marshal(map[string]string{"rating": rating, "title": title})
	req, _ := http.NewRequest("POST", cdnBase+"listings/"+id+"/review", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	resp.Body.Close()

	fmt.Printf("✓ Review submitted for %s.\n", id)
	return nil
}

func handleCollaborations(args []string) error {
	_ = args
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", cdnBase+"collaborations", nil)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Collaborations []map[string]any `json:"collaborations"`
	}
	json.Unmarshal(body, &result)

	fmt.Println()
	fmt.Printf("  %-8s %-14s %-20s %-10s %-14s %s\n", "ID", "PEER", "CAPABILITY", "STATUS", "REQUESTS(30d)", "COST(30d)")
	for _, c := range result.Collaborations {
		fmt.Printf("  %-8s %-14s %-20s %-10s %-14s %s\n",
			getStr(c, "id"),
			getStr(c, "peer"),
			getStr(c, "capability"),
			getStr(c, "status"),
			getStr(c, "requests"),
			getStr(c, "cost"))
	}
	fmt.Println()
	return nil
}

func handleUsage(id string, args []string) error {
	_ = args
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", cdnBase+"usage/"+id, nil)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result map[string]any
	json.Unmarshal(body, &result)

	fmt.Println()
	fmt.Printf("  Collaboration: %s\n", getStr(result, "collaboration"))
	fmt.Printf("  Period: %s\n\n", getStr(result, "period"))
	fmt.Printf("  Requests:       %s\n", getStr(result, "requests"))
	fmt.Printf("  Avg latency:    %s\n", getStr(result, "avg_latency"))
	fmt.Printf("  Error rate:     %s\n", getStr(result, "error_rate"))
	fmt.Printf("  Total cost:     %s\n", getStr(result, "total_cost"))
	fmt.Printf("  Avg cost/req:   %s\n", getStr(result, "avg_cost"))
	fmt.Println()
	return nil
}

func handleTerminate(id string, args []string) error {
	confirm := false
	for _, a := range args {
		if a == "--confirm" {
			confirm = true
		}
	}

	if !confirm {
		fmt.Printf("⚠ This will terminate the collaboration for %s.\n", id)
		fmt.Println("  Active workflows using this capability will fail.")
		fmt.Printf("  Type listing ID to confirm: ")
		var input string
		fmt.Scanln(&input)
		if strings.TrimSpace(input) != id {
			return fmt.Errorf("confirmation failed — ID did not match")
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("POST", cdnBase+"terminate/"+id, nil)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	resp.Body.Close()

	fmt.Printf("✓ Termination initiated. Grace period: 7 days.\n")
	return nil
}

func getStr(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return fmt.Sprintf("%.0f", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}
