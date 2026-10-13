package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
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
	Audio        string `json:"audio"`
	AudioCache   string `json:"input_audio_cache"`
	AudioOutput  string `json:"audio_output"`
}
type openRouterOverride struct {
	openRouterPrices
	MinPromptTokens *int64   `json:"min_prompt_tokens"`
	UTCDays         []string `json:"utc_days"`
	UTCStart        *int     `json:"utc_start"`
	UTCEnd          *int     `json:"utc_end"`
}
type openRouterPricing struct {
	openRouterPrices
	Overrides []openRouterOverride `json:"overrides"`
}

// Preserve provider context, UTC schedule and modality prices in one contract.
func openRouterExpression(id string, pricing openRouterPricing) (string, error) {
	if strings.Contains(strings.ToLower(id), "claude") && (pricing.CacheRead == "" || pricing.CacheWrite == "" || pricing.CacheWrite1h == "") {
		return "", errors.New("Claude cache prices are incomplete")
	}
	overrides := slices.Clone(pricing.Overrides)
	timed := false
	for _, o := range overrides {
		if len(o.UTCDays) > 0 || o.UTCStart != nil || o.UTCEnd != nil {
			timed = true
		}
	}
	for _, o := range overrides {
		if timed && o.MinPromptTokens != nil {
			return "", errors.New("mixed context and time overrides are unsupported")
		}
		if !timed && (o.MinPromptTokens == nil || *o.MinPromptTokens <= 0 || *o.MinPromptTokens > 2147483647) {
			return "", errors.New("invalid context threshold")
		}
	}
	if !timed {
		slices.SortFunc(overrides, func(a, b openRouterOverride) int {
			if *a.MinPromptTokens < *b.MinPromptTokens {
				return -1
			}
			if *a.MinPromptTokens > *b.MinPromptTokens {
				return 1
			}
			return 0
		})
	}
	prices := []openRouterPrices{pricing.openRouterPrices}
	conditions := []string{}
	labels := []string{"standard"}
	previous := int64(0)
	weekdays := map[string]int{"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3, "thursday": 4, "friday": 5, "saturday": 6}
	for i, o := range overrides {
		parent := prices[len(prices)-1]
		if timed {
			parent = pricing.openRouterPrices
			parts := []string{}
			if len(o.UTCDays) > 0 {
				days := []int{}
				seen := map[int]bool{}
				for _, name := range o.UTCDays {
					day, ok := weekdays[name]
					if !ok || seen[day] {
						return "", errors.New("invalid UTC days")
					}
					seen[day] = true
					days = append(days, day)
				}
				slices.Sort(days)
				tests := []string{}
				for _, day := range days {
					tests = append(tests, fmt.Sprintf(`weekday("UTC") == %d`, day))
				}
				parts = append(parts, "("+strings.Join(tests, " || ")+")")
			}
			if o.UTCStart != nil || o.UTCEnd != nil {
				if o.UTCStart == nil || o.UTCEnd == nil {
					return "", errors.New("incomplete UTC interval")
				}
				start, end := *o.UTCStart, *o.UTCEnd
				if start < 0 || start > 2359 || start%100 > 59 || end < 0 || end > 2359 || end%100 > 59 || start == end {
					return "", errors.New("invalid UTC interval")
				}
				joiner := " && "
				if start > end {
					joiner = " || "
				}
				clock := `(hour("UTC") * 100 + minute("UTC"))`
				parts = append(parts, fmt.Sprintf("(%s >= %d%s%s < %d)", clock, start, joiner, clock, end))
			}
			if len(parts) == 0 {
				return "", errors.New("empty time condition")
			}
			conditions = append(conditions, strings.Join(parts, " && "))
			labels = append(labels, fmt.Sprintf("utc_%d", i+1))
		} else {
			threshold := *o.MinPromptTokens
			if threshold <= previous {
				return "", errors.New("duplicate context threshold")
			}
			previous = threshold
			conditions = append(conditions, fmt.Sprintf("len < %d", threshold))
			labels = append(labels, "context_"+strconv.FormatInt(threshold, 10))
		}
		for _, f := range []struct {
			target *string
			value  string
		}{
			{&parent.Prompt, o.Prompt}, {&parent.Completion, o.Completion}, {&parent.CacheRead, o.CacheRead}, {&parent.CacheWrite, o.CacheWrite}, {&parent.CacheWrite1h, o.CacheWrite1h}, {&parent.Audio, o.Audio}, {&parent.AudioCache, o.AudioCache}, {&parent.AudioOutput, o.AudioOutput},
		} {
			if f.value != "" {
				*f.target = f.value
			}
		}
		prices = append(prices, parent)
	}
	useAudio, useAudioCache, useAudioOutput := false, false, false
	sameRate := func(a, b string) bool {
		left, le := decimal.NewFromString(a)
		right, re := decimal.NewFromString(b)
		return le == nil && re == nil && left.Equal(right)
	}
	for _, p := range prices {
		if p.Audio != "" && !sameRate(p.Audio, p.Prompt) {
			useAudio = true
		}
		if p.AudioCache != "" && !sameRate(p.AudioCache, p.CacheRead) {
			useAudioCache = true
		}
		if p.AudioOutput != "" {
			useAudioOutput = true
		}
	}
	if useAudio {
		for _, p := range prices {
			if p.AudioCache != "" {
				useAudioCache = true
			}
		}
	}
	leaves := []string{}
	present := map[string]int{}
	for index, p := range prices {
		fields := []struct {
			name, value string
			required    bool
		}{{"p", p.Prompt, true}, {"c", p.Completion, true}, {"cr", p.CacheRead, false}, {"cc", p.CacheWrite, false}, {"cc1h", p.CacheWrite1h, false}}
		if useAudio {
			fields = append(fields, struct {
				name, value string
				required    bool
			}{"ai", p.Audio, true})
		}
		if useAudioCache {
			fields = append(fields, struct {
				name, value string
				required    bool
			}{"ai_cr", p.AudioCache, true})
		}
		if useAudioOutput {
			fields = append(fields, struct {
				name, value string
				required    bool
			}{"ao", p.AudioOutput, true})
		}
		terms := []string{}
		for _, f := range fields {
			if f.value == "" && !f.required {
				continue
			}
			number, err := decimal.NewFromString(f.value)
			if err != nil || number.IsNegative() || number.GreaterThan(decimal.NewFromInt(1000)) {
				return "", errors.New("invalid or incomplete token price")
			}
			terms = append(terms, f.name+" * "+number.Mul(decimal.NewFromInt(1000000)).String())
			present[f.name]++
		}
		leaves = append(leaves, "tier("+strconv.Quote(labels[index])+", "+strings.Join(terms, " + ")+")")
	}
	for _, n := range present {
		if n != len(prices) {
			return "", errors.New("incomplete category across pricing tiers")
		}
	}
	expression := leaves[len(leaves)-1]
	if timed {
		expression = leaves[0]
		for i := len(conditions) - 1; i >= 0; i-- {
			expression = conditions[i] + " ? " + leaves[i+1] + " : " + expression
		}
	} else {
		for i := len(conditions) - 1; i >= 0; i-- {
			expression = conditions[i] + " ? " + leaves[i] + " : " + expression
		}
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
	return readModelPriceCatalog(response.Body)
}

func readModelPriceCatalog(reader io.Reader) (map[string]string, error) {
	var catalog struct {
		SchemaVersion  int `json:"schema_version"`
		SourceMetadata map[string]struct {
			Pricing    openRouterPricing `json:"pricing"`
			Source     string            `json:"source"`
			SourceURL  string            `json:"source_url"`
			Expression string            `json:"expression"`
			Unit       string            `json:"unit"`
		} `json:"source_metadata"`
		Success bool `json:"success"`
		Data    struct {
			BillingMode map[string]string `json:"billing_mode"`
			BillingExpr map[string]string `json:"billing_expr"`
		} `json:"data"`
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxRatioConfigBytes+1))
	if err != nil || len(data) > maxRatioConfigBytes {
		return nil, errors.New("model-prices catalog is too large or unreadable")
	}
	if err := common.Unmarshal(data, &catalog); err != nil || !catalog.Success || (catalog.SchemaVersion != 1 && catalog.SchemaVersion != 2) {
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
		if catalog.SchemaVersion == 2 && metadata.Source == "official_manual" {
			reference, err := url.Parse(metadata.SourceURL)
			if err != nil || reference.Scheme != "https" || reference.Hostname() == "" || reference.User != nil || metadata.Expression != expression {
				return nil, fmt.Errorf("model-prices model %s: invalid manual source", name)
			}
			if _, err := billingexpr.CompileFromCache(expression); err != nil {
				return nil, err
			}
			if metadata.Unit == "task_usage" {
				if len(billingexpr.UsedUsageKeys(expression)) == 0 || billingexpr.UsesFixedPricing(expression) {
					return nil, errors.New("invalid task usage contract")
				}
			} else if metadata.Unit == "token" || metadata.Unit == "request" || metadata.Unit == "measured" {
				if len(billingexpr.UsedUsageKeys(expression)) != 0 || (metadata.Unit == "request") != billingexpr.UsesFixedPricing(expression) {
					return nil, errors.New("manual pricing unit does not match expression")
				}
				if metadata.Unit == "measured" {
					used := billingexpr.UsedVars(expression)
					if !used["audio_seconds"] && !used["text_chars"] {
						return nil, errors.New("measured pricing requires duration or character usage")
					}
				}
				if err := billing_setting.SmokeTestExpr(expression); err != nil {
					return nil, err
				}
			} else {
				return nil, errors.New("invalid manual pricing unit")
			}
			result[name] = expression
			continue
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
