package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type openRouterPrices struct {
	Prompt       string `json:"prompt"`
	Completion   string `json:"completion"`
	CacheRead    string `json:"input_cache_read"`
	CacheWrite   string `json:"input_cache_write"`
	CacheWrite1h string `json:"input_cache_write_1h"`
}

type openRouterPricing struct {
	openRouterPrices
	Overrides []struct {
		openRouterPrices
		MinPromptTokens int64 `json:"min_prompt_tokens"`
	} `json:"overrides"`
}

// Generate a complete local contract. Missing/invalid Claude cache categories
// and invalid tiers reject that model rather than publishing partial prices.
func openRouterExpression(id string, pricing openRouterPricing) (string, error) {
	if strings.Contains(strings.ToLower(id), "claude") && (pricing.CacheRead == "" || pricing.CacheWrite == "" || pricing.CacheWrite1h == "") {
		return "", errors.New("Claude cache prices are incomplete")
	}
	tiers := slices.Clone(pricing.Overrides)
	slices.SortFunc(tiers, func(a, b struct {
		openRouterPrices
		MinPromptTokens int64 `json:"min_prompt_tokens"`
	}) int {
		if a.MinPromptTokens < b.MinPromptTokens {
			return -1
		}
		if a.MinPromptTokens > b.MinPromptTokens {
			return 1
		}
		return 0
	})
	previous := int64(0)
	prices := []openRouterPrices{pricing.openRouterPrices}
	for _, override := range tiers {
		if override.MinPromptTokens <= previous || override.MinPromptTokens > 2147483647 {
			return "", errors.New("invalid or duplicate context threshold")
		}
		previous = override.MinPromptTokens
		next := prices[len(prices)-1]
		for _, field := range []struct {
			target *string
			value  string
		}{
			{&next.Prompt, override.Prompt}, {&next.Completion, override.Completion},
			{&next.CacheRead, override.CacheRead}, {&next.CacheWrite, override.CacheWrite}, {&next.CacheWrite1h, override.CacheWrite1h},
		} {
			if field.value != "" {
				*field.target = field.value
			}
		}
		prices = append(prices, next)
	}
	leaves := make([]string, len(prices))
	for index, price := range prices {
		terms := []string{}
		for _, field := range []struct {
			name, value string
			required    bool
		}{
			{"p", price.Prompt, true}, {"c", price.Completion, true},
			{"cr", price.CacheRead, false}, {"cc", price.CacheWrite, false}, {"cc1h", price.CacheWrite1h, false},
		} {
			if field.value == "" && !field.required {
				continue
			}
			number, err := decimal.NewFromString(field.value)
			if err != nil || number.IsNegative() || number.GreaterThan(decimal.NewFromInt(1000)) {
				return "", errors.New("invalid token price")
			}
			terms = append(terms, field.name+" * "+number.Mul(decimal.NewFromInt(1000000)).String())
		}
		label := "standard"
		if index > 0 {
			label = "context_" + strconv.FormatInt(tiers[index-1].MinPromptTokens, 10)
		}
		leaves[index] = "tier(" + strconv.Quote(label) + ", " + strings.Join(terms, " + ") + ")"
	}
	expression := leaves[len(leaves)-1]
	for i := len(tiers) - 1; i >= 0; i-- {
		expression = "len < " + strconv.FormatInt(tiers[i].MinPromptTokens, 10) + " ? " + leaves[i] + " : " + expression
	}
	if err := billing_setting.SmokeTestExpr(expression); err != nil {
		return "", err
	}
	return expression, nil
}

func readOpenRouterExpressions(reader io.Reader) (map[string]string, error) {
	var response struct {
		Data []struct {
			ID      string            `json:"id"`
			Pricing openRouterPricing `json:"pricing"`
		} `json:"data"`
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxRatioConfigBytes+1))
	if err != nil || len(data) > maxRatioConfigBytes {
		return nil, errors.New("OpenRouter pricing catalog is too large or unreadable")
	}
	if err := common.Unmarshal(data, &response); err != nil {
		return nil, errors.New("invalid OpenRouter pricing catalog")
	}
	result := map[string]string{}
	for _, entry := range response.Data {
		if entry.ID == "" {
			continue
		}
		expression, err := openRouterExpression(entry.ID, entry.Pricing)
		if err == nil {
			result[entry.ID] = expression
		}
	}
	if len(result) == 0 {
		return nil, errors.New("OpenRouter returned no complete token prices")
	}
	return result, nil
}

const modelPriceCatalogURL = "https://raw.githubusercontent.com/datehoer/model-prices/main/newapi-pricing.json"

func fetchModelPriceCatalog(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, modelPriceCatalogURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, errors.New("model-prices catalog could not be fetched; saved prices are retained")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model-prices catalog returned HTTP %d; saved prices are retained", response.StatusCode)
	}
	var catalog struct {
		SchemaVersion  int `json:"schema_version"`
		SourceMetadata map[string]struct {
			Pricing openRouterPricing `json:"pricing"`
		} `json:"source_metadata"`
		Success bool `json:"success"`
		Data    struct {
			BillingMode map[string]string `json:"billing_mode"`
			BillingExpr map[string]string `json:"billing_expr"`
		} `json:"data"`
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxRatioConfigBytes+1))
	if err != nil || len(data) > maxRatioConfigBytes {
		return nil, errors.New("model-prices catalog is too large or unreadable")
	}
	if err := common.Unmarshal(data, &catalog); err != nil || !catalog.Success || catalog.SchemaVersion != 1 {
		return nil, errors.New("invalid model-prices catalog")
	}
	result := map[string]string{}
	for name, expression := range catalog.Data.BillingExpr {
		if catalog.Data.BillingMode[name] != billing_setting.BillingModeTieredExpr {
			return nil, errors.New("model-prices catalog contains incomplete pricing")
		}
		metadata, exists := catalog.SourceMetadata[name]
		if !exists {
			return nil, errors.New("model-prices catalog is missing price metadata")
		}
		expected, err := openRouterExpression(name, metadata.Pricing)
		if err != nil || expression != expected {
			return nil, fmt.Errorf("model-prices model %s: incomplete or inconsistent price", name)
		}
		result[name] = expression
	}
	if len(result) == 0 {
		return nil, errors.New("model-prices returned no complete token prices")
	}
	return result, nil
}

func buildModelPriceSourceChanges(snapshot *model.ModelPricingSnapshot, bindings, expressions map[string]string, automatic bool) ([]model.ModelPricingChange, error) {
	entries := map[string]model.ModelPricingEntry{}
	for _, entry := range snapshot.Entries {
		entries[entry.ModelName] = entry
	}
	names := map[string]bool{}
	for name := range bindings {
		names[name] = true
	}
	if !automatic {
		for name, entry := range entries {
			if _, linked := entry.Configured[billing_setting.PriceSourceOption]; linked {
				names[name] = true
			}
		}
	}
	changes := []model.ModelPricingChange{}
	for _, name := range slices.Sorted(maps.Keys(names)) {
		entry := entries[name]
		pricing := maps.Clone(entry.Configured)
		if pricing == nil {
			pricing = model.PricingValues{}
		}
		version := entry.Version
		if version == "" {
			version = snapshot.EmptyVersion
		}
		source, exists := bindings[name]
		if !exists {
			delete(pricing, billing_setting.PriceSourceOption)
			delete(pricing, billing_setting.PriceSourceExprOption)
			delete(pricing, billing_setting.PriceSourceUpdatedOption)
			changes = append(changes, model.ModelPricingChange{ModelName: name, ExpectedVersion: version, Pricing: pricing})
			continue
		}
		if strings.TrimSpace(name) != name || name == "" || len(name) > 512 || source == "" || strings.ContainsAny(source, "\r\n\t ") {
			return nil, errors.New("enter an exact local model and model-prices model ID")
		}
		if automatic && (pricing["billing_setting.billing_mode"] != billing_setting.BillingModeTieredExpr || pricing["billing_setting.billing_expr"] != pricing[billing_setting.PriceSourceExprOption]) {
			continue
		}
		expression, ok := expressions[source]
		if !ok {
			return nil, fmt.Errorf("model %s: model-prices model %s has no complete supported price; saved prices are retained", name, source)
		}
		if automatic && pricing["billing_setting.billing_expr"] == expression {
			continue
		}
		pricing["billing_setting.billing_mode"] = billing_setting.BillingModeTieredExpr
		pricing["billing_setting.billing_expr"] = expression
		pricing[billing_setting.PriceSourceOption] = "model-prices:" + source
		pricing[billing_setting.PriceSourceExprOption] = expression
		pricing[billing_setting.PriceSourceUpdatedOption] = time.Now().UTC().Format(time.RFC3339)
		changes = append(changes, model.ModelPricingChange{ModelName: name, ExpectedVersion: version, Pricing: pricing})
	}
	return changes, nil
}

// Preview does not write: the existing version-checked model-pricing API applies
// exactly this reviewed contract, including source metadata in one transaction.
func PreviewModelPriceSourceSync(c *gin.Context) {
	var request struct {
		Bindings map[string]string `json:"bindings"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.Bindings == nil || len(request.Bindings) > 1000 {
		common.ApiError(c, errors.New("invalid price bindings"))
		return
	}
	snapshot, err := model.GetModelPricingSnapshot(nil)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	expressions := map[string]string{}
	if len(request.Bindings) > 0 {
		expressions, err = fetchModelPriceCatalog(c.Request.Context())
		if err != nil {
			common.ApiError(c, err)
			return
		}
	}
	changes, err := buildModelPriceSourceChanges(snapshot, request.Bindings, expressions, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"changes": changes})
}

type modelPriceSourceSyncHandler struct{}

func (modelPriceSourceSyncHandler) Type() string { return "price_sync" }
func (modelPriceSourceSyncHandler) Enabled() bool {
	return len(billing_setting.GetPriceSourceCopy()) > 0
}
func (modelPriceSourceSyncHandler) Interval() time.Duration { return 24 * time.Hour }
func (modelPriceSourceSyncHandler) NewPayload() any         { return nil }
func (modelPriceSourceSyncHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	snapshot, err := model.GetModelPricingSnapshot(nil)
	bindings := map[string]string{}
	if err == nil {
		for _, entry := range snapshot.Entries {
			if source, ok := entry.Configured[billing_setting.PriceSourceOption].(string); ok {
				bindings[entry.ModelName] = strings.TrimPrefix(source, "model-prices:")
			}
		}
	}
	var changes []model.ModelPricingChange
	if err == nil && len(bindings) > 0 {
		var expressions map[string]string
		expressions, err = fetchModelPriceCatalog(ctx)
		if err == nil {
			changes, err = buildModelPriceSourceChanges(snapshot, bindings, expressions, true)
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil && len(changes) > 0 {
			err = model.UpdateModelPricing(changes)
		}
	}
	status := model.SystemTaskStatusSucceeded
	if err != nil {
		status = model.SystemTaskStatusFailed
	}
	applied := changes
	if err != nil {
		applied = nil
	}
	finishSystemTaskHandler(task, runnerID, status, map[string]any{"applied_changes": applied, "source": "model-prices", "checked_at": time.Now().UTC().Format(time.RFC3339)}, err)
}
