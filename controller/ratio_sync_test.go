package controller

import (
	"fmt"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
)

func TestPricingSyncExpressionPriority(t *testing.T) {
	expression := `tier("base", p * 2 + c * 8 + cr * 0)`
	cases := []struct {
		name       string
		local      map[string]any
		source     map[string]any
		wantFields []string
	}{
		{"equal expressions suppress stale ratios", map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": expression}}, map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": expression}, "model_ratio": map[string]float64{"m": 3}, "model_price": map[string]float64{"m": 2}}, nil},
		{"local expression excludes legacy-only source", map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": expression}}, map[string]any{"model_ratio": map[string]float64{"m": 3}, "completion_ratio": map[string]float64{"m": 2}}, nil},
		{"expression imports without legacy conflicts", map[string]any{"model_ratio": map[string]float64{"m": 1}}, map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": expression}, "model_ratio": map[string]float64{"m": 3}, "model_price": map[string]float64{"m": 2}}, []string{"billing_mode", "billing_expr"}},
		{"inactive expression follows explicit ratio mode", map[string]any{"model_ratio": map[string]float64{"m": 1}}, map[string]any{"billing_mode": map[string]string{"m": "ratio"}, "billing_expr": map[string]string{"m": expression}, "model_ratio": map[string]float64{"m": 3}}, []string{"model_ratio"}},
		{"empty active expression never imports a false free price", map[string]any{}, map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": " "}, "model_ratio": map[string]float64{"m": 0}}, nil},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			diff := buildDifferences(tt.local, []struct {
				name string
				data map[string]any
			}{{"source", tt.source}})
			fields := make([]string, 0, len(diff["m"]))
			for field := range diff["m"] {
				fields = append(fields, field)
			}
			assert.ElementsMatch(t, tt.wantFields, fields)
		})
	}
}

func TestRatioConfigExportsEffectiveExpressions(t *testing.T) {
	before := config.GlobalConfig.ExportAllConfigs()
	expose := ratio_setting.IsExposeRatioEnabled()
	t.Cleanup(func() {
		config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"billing_mode": before["billing_setting.billing_mode"], "billing_expr": before["billing_setting.billing_expr"]})
		ratio_setting.SetExposeRatioEnabled(expose)
	})
	config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"billing_mode": `{"sync-export":"tiered_expr"}`, "billing_expr": `{"sync-export":"tier(\"base\", p * 2)"}`})
	ratio_setting.SetExposeRatioEnabled(true)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/ratio_config", nil)
	GetRatioConfig(c)
	var response struct {
		Success bool
		Data    map[string]any
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, billing_setting.BillingModeTieredExpr, valueMap(response.Data["billing_mode"])["sync-export"])
	assert.Equal(t, `tier("base", p * 2)`, valueMap(response.Data["billing_expr"])["sync-export"])
}

func TestPricingSyncCompleteSourcesAndArrayFormats(t *testing.T) {
	before := config.GlobalConfig.ExportAllConfigs()
	oldRatios, oldCompletion := ratio_setting.ModelRatio2JSONString(), ratio_setting.CompletionRatio2JSONString()
	t.Cleanup(func() {
		config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"billing_mode": before["billing_setting.billing_mode"], "billing_expr": before["billing_setting.billing_expr"]})
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatios))
		require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(oldCompletion))
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"sync-token":1}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"sync-token":2}`))
	expression := `len <= 200000 ? tier("short", p * 2 + c * 8 + cr * 0) : tier("long", p * 4 + c * 12)`
	expressions, err := common.Marshal(map[string]string{"sync-already": expression})
	require.NoError(t, err)
	config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"billing_mode": `{"sync-already":"tiered_expr"}`, "billing_expr": string(expressions)})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		if r.URL.Path == "/ratio_config" {
			data = map[string]any{
				"billing_mode":     map[string]string{"sync-already": "tiered_expr", "sync-expression": "tiered_expr"},
				"billing_expr":     map[string]string{"sync-already": expression, "sync-expression": expression},
				"model_ratio":      map[string]float64{"sync-already": 9, "sync-expression": 9, "sync-token": 1},
				"model_price":      map[string]float64{"sync-expression": 4},
				"completion_ratio": map[string]float64{"sync-token": 4},
				"cache_ratio":      map[string]float64{"sync-token": 0},
			}
		} else {
			data = []map[string]any{
				{"model_name": "sync-already", "model_ratio": 5, "completion_ratio": 3},
				{"model_name": "sync-expression", "model_ratio": 2, "model_price": 1},
				{"model_name": "sync-array-expression", "billing_mode": "tiered_expr", "billing_expr": expression, "quota_type": 1, "model_price": 0},
				{"model_name": "sync-unpriced"},
				{"model_name": "sync-invalid-expression", "billing_mode": "tiered_expr", "billing_expr": "", "model_ratio": 0},
				{"model_name": "sync-free", "model_ratio": 0, "completion_ratio": 0},
			}
		}
		encoded, err := common.Marshal(map[string]any{"success": true, "data": data})
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encoded)
	}))
	defer server.Close()
	var response struct {
		Success bool
		Data    struct {
			Differences map[string]map[string]dto.DifferenceItem
			Prices      map[string]struct {
				Current   map[string]any
				Upstreams map[string]map[string]any
			}
			TestResults []dto.TestResult `json:"test_results"`
		}
	}
	body := map[string]any{"upstreams": []map[string]any{
		{"id": 1, "name": "Expressions", "base_url": server.URL, "endpoint": "/ratio_config"},
		{"id": 2, "name": "Legacy", "base_url": server.URL, "endpoint": "/pricing"},
	}}
	recorder := modelManagementRequest(t, FetchUpstreamRatios, http.MethodPost, "/api/channel/fetch_upstream_ratios", body, &response)
	require.True(t, response.Success, recorder.Body.String())
	require.Len(t, response.Data.TestResults, 2)
	for _, result := range response.Data.TestResults {
		require.Equal(t, "success", result.Status, result.Error)
	}
	assert.NotContains(t, response.Data.Differences, "sync-already")
	assert.NotContains(t, response.Data.Differences, "sync-unpriced")
	assert.NotContains(t, response.Data.Differences, "sync-invalid-expression")
	assert.Equal(t, map[string]any{"billing_mode": "tiered_expr", "billing_expr": expression}, response.Data.Prices["sync-expression"].Upstreams["Expressions(1)"])
	assert.NotContains(t, response.Data.Prices["sync-expression"].Upstreams, "Legacy(2)")
	assert.Equal(t, map[string]any{"billing_mode": "tiered_expr", "billing_expr": expression}, response.Data.Prices["sync-array-expression"].Upstreams["Legacy(2)"])
	assert.Equal(t, float64(1), response.Data.Prices["sync-token"].Upstreams["Expressions(1)"]["model_ratio"], "unchanged base prices are included for a complete price preview")
	assert.Equal(t, float64(4), response.Data.Prices["sync-token"].Upstreams["Expressions(1)"]["completion_ratio"])
	assert.Equal(t, float64(0), response.Data.Prices["sync-token"].Upstreams["Expressions(1)"]["cache_ratio"])
	assert.Equal(t, float64(0), response.Data.Prices["sync-free"].Upstreams["Legacy(2)"]["model_ratio"])
}

func TestOpenRouterCompleteExpressions(t *testing.T) {
	const catalog = `{"data":[{"id":"anthropic/claude-sonnet-5.5","pricing":{"prompt":"0.000002","completion":"0.00001","input_cache_read":"0.0000001","input_cache_write":"0.0000025","input_cache_write_1h":"0.000004","overrides":[{"min_prompt_tokens":100000,"prompt":"0.000004","input_cache_read":"0"}]}}]}`
	converted, err := convertOpenRouterToRatioData(strings.NewReader(catalog))
	require.NoError(t, err)
	expression, ok := valueMap(converted["billing_expr"])["anthropic/claude-sonnet-5.5"].(string)
	require.True(t, ok)
	assert.NotContains(t, converted, "model_ratio")
	assert.NotContains(t, valueMap(converted["billing_expr"]), "claude-sonnet-5-5")
	for _, tc := range []struct {
		length float64
		want   float64
		tier   string
	}{{99999, 30.6, "standard"}, {100000, 32.5, "context_100000"}} {
		cost, trace, err := billingexpr.RunExpr(expression, billingexpr.TokenParams{P: 1, C: 1, CR: 1, CC: 1, CC1h: 4, Len: tc.length})
		require.NoError(t, err)
		assert.InDelta(t, tc.want, cost, 0.00000001)
		assert.Equal(t, tc.tier, trace.MatchedTier)
	}
	for _, input := range []string{
		`{"data":[{"id":"claude","pricing":{"prompt":"0.1","completion":"0.2"}}]}`,
		`{"data":[{"id":"m","pricing":{"prompt":"NaN","completion":"0"}}]}`,
		`{"data":[{"id":"m","pricing":{"prompt":"-1","completion":"0"}}]}`,
		`{"data":[{"id":"m","pricing":{"prompt":"0","completion":"0","overrides":[{"min_prompt_tokens":1},{"min_prompt_tokens":1}]}}]}`,
	} {
		_, err := readOpenRouterExpressions(strings.NewReader(input))
		require.Error(t, err)
	}
	zero, err := readOpenRouterExpressions(strings.NewReader(`{"data":[{"id":"m","pricing":{"prompt":"0","completion":"0","input_cache_read":"0"}}]}`))
	require.NoError(t, err)
	assert.Equal(t, `tier("standard", p * 0 + c * 0 + cr * 0)`, zero["m"])
}

func TestModelPriceSourceDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env)
			}
			modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			savedConfig := config.GlobalConfig.ExportAllConfigs()
			t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(savedConfig)) })
			name := "claude-local"
			expr := `tier("standard", p * 2 + c * 10 + cr * 0.1 + cc * 2.5 + cc1h * 4)`
			snapshot, err := model.GetModelPricingSnapshot([]string{name})
			require.NoError(t, err)
			changes, err := buildModelPriceSourceChanges(snapshot, map[string]string{name: "claude-sonnet-5-5"}, map[string]string{"claude-sonnet-5-5": expr}, false)
			require.NoError(t, err)
			require.Len(t, changes, 1)
			require.NoError(t, model.UpdateModelPricing(changes))
			snapshot, err = model.GetModelPricingSnapshot([]string{name})
			require.NoError(t, err)
			require.Len(t, snapshot.Entries, 1)
			entry := snapshot.Entries[0]
			assert.Equal(t, expr, entry.Configured["billing_setting.billing_expr"])
			assert.Equal(t, "model-prices:claude-sonnet-5-5", entry.Configured[billing_setting.PriceSourceOption])
			assert.NotEmpty(t, entry.Configured[billing_setting.PriceSourceUpdatedOption])
			assert.ErrorIs(t, model.UpdateModelPricing(changes), model.ErrModelPricingConflict)
			changes, err = buildModelPriceSourceChanges(snapshot, map[string]string{name: "claude-sonnet-5-5"}, map[string]string{"claude-sonnet-5-5": expr}, true)
			require.NoError(t, err)
			assert.Empty(t, changes)
			changes, err = buildModelPriceSourceChanges(snapshot, map[string]string{name: "missing"}, map[string]string{}, true)
			require.Error(t, err)
			assert.Empty(t, changes)
			nextExpr := `tier("standard", p * 3 + c * 15 + cr * 0.15 + cc * 3.75 + cc1h * 6)`
			changes, err = buildModelPriceSourceChanges(snapshot, map[string]string{name: "claude-sonnet-5-5"}, map[string]string{"claude-sonnet-5-5": nextExpr}, true)
			require.NoError(t, err)
			require.Len(t, changes, 1)
			require.NoError(t, model.UpdateModelPricing(changes))
			snapshot, err = model.GetModelPricingSnapshot([]string{name})
			require.NoError(t, err)
			manual := snapshot.Entries[0].Configured
			manual["billing_setting.billing_expr"] = `tier("manual", p * 1)`
			require.NoError(t, model.UpdateModelPricing([]model.ModelPricingChange{{ModelName: name, ExpectedVersion: snapshot.Entries[0].Version, Pricing: manual}}))
			snapshot, err = model.GetModelPricingSnapshot([]string{name})
			require.NoError(t, err)
			assert.NotContains(t, snapshot.Entries[0].Configured, billing_setting.PriceSourceOption)
			assert.Equal(t, `tier("manual", p * 1)`, snapshot.Entries[0].Configured["billing_setting.billing_expr"])
			// Relink, then detach without changing the saved expression.
			changes, err = buildModelPriceSourceChanges(snapshot, map[string]string{name: "claude-sonnet-5-5"}, map[string]string{"claude-sonnet-5-5": expr}, false)
			require.NoError(t, err)
			require.NoError(t, model.UpdateModelPricing(changes))
			snapshot, err = model.GetModelPricingSnapshot([]string{name})
			require.NoError(t, err)
			changes, err = buildModelPriceSourceChanges(snapshot, map[string]string{}, nil, false)
			require.NoError(t, err)
			require.NoError(t, model.UpdateModelPricing(changes))
			snapshot, err = model.GetModelPricingSnapshot([]string{name})
			require.NoError(t, err)
			assert.NotContains(t, snapshot.Entries[0].Configured, billing_setting.PriceSourceOption)
			assert.Equal(t, expr, snapshot.Entries[0].Configured["billing_setting.billing_expr"])
			changes, err = buildModelPriceSourceChanges(snapshot, map[string]string{name: "claude-sonnet-5-5"}, map[string]string{"claude-sonnet-5-5": expr}, false)
			require.NoError(t, err)
			require.NoError(t, model.UpdateModelPricing(changes))
			raw, err := common.Marshal(map[string]string{name: `tier("legacy-manual", p * 4)`})
			require.NoError(t, err)
			require.NoError(t, model.UpdateModelPricingOptions(map[string]string{"billing_setting.billing_expr": string(raw)}))
			snapshot, err = model.GetModelPricingSnapshot([]string{name})
			require.NoError(t, err)
			assert.NotContains(t, snapshot.Entries[0].Configured, billing_setting.PriceSourceOption)
		})
	}
}

func TestOpenRouterTimeAndAudioPrices(t *testing.T) {
	input := `{"data":[{"id":"scheduled","pricing":{"prompt":"0.000002","completion":"0.00001","input_cache_read":"0.0000001","overrides":[{"utc_days":["saturday","sunday"],"prompt":"0.000001"},{"utc_start":2300,"utc_end":100,"completion":"0.000005"}]}},{"id":"audio","pricing":{"prompt":"0.000002","completion":"0.00001","input_cache_read":"0.0000001","audio":"0.000005","input_audio_cache":"0.0000005"}}]}`
	prices, err := readOpenRouterExpressions(strings.NewReader(input))
	require.NoError(t, err)
	for _, tc := range []struct {
		name              string
		day, hour, minute int
		want              float64
	}{
		{"weekend", 6, 12, 0, 2010}, {"weekday night", 2, 23, 30, 2510}, {"night end is exclusive", 2, 1, 0, 3010},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expr := strings.NewReplacer(`weekday("UTC")`, fmt.Sprint(tc.day), `hour("UTC")`, fmt.Sprint(tc.hour), `minute("UTC")`, fmt.Sprint(tc.minute)).Replace(prices["scheduled"])
			cost, _, err := billingexpr.RunExpr(expr, billingexpr.TokenParams{P: 1000, C: 100, CR: 100})
			require.NoError(t, err)
			assert.InDelta(t, tc.want, cost, 0.00001)
		})
	}
	cost, _, err := billingexpr.RunExpr(prices["audio"], billingexpr.TokenParams{P: 100, C: 10, CR: 100, AI: 50, AICR: 100})
	require.NoError(t, err)
	assert.Equal(t, float64(610), cost)
	for _, override := range []string{`{"utc_start":2360,"utc_end":100}`, `{"utc_start":100}`, `{"utc_days":["sunday","sunday"]}`, `{"min_prompt_tokens":100,"utc_days":["sunday"]}`} {
		_, err := readOpenRouterExpressions(strings.NewReader(`{"data":[{"id":"bad","pricing":{"prompt":"0","completion":"0","overrides":[` + override + `]}}]}`))
		require.Error(t, err)
	}
}

func TestManualMaintainedPriceCatalog(t *testing.T) {
	expression := `tier("standard", p * 2 + c * 10)`
	for _, tc := range []struct {
		name, source, unit, metadataExpression, expression string
		valid                                              bool
	}{
		{"token contract", "https://example.org/pricing", "token", expression, expression, true},
		{"request contract", "https://example.org/pricing", "request", `tier("standard", fixed(0.1))`, `tier("standard", fixed(0.1))`, true},
		{"fixed token mismatch", "https://example.org/pricing", "token", `tier("standard", fixed(0.1))`, `tier("standard", fixed(0.1))`, false},
		{"token request mismatch", "https://example.org/pricing", "request", expression, expression, false},
		{"task contract", "https://example.org/pricing", "task_usage", `u("seconds") * 0.1`, `u("seconds") * 0.1`, true},
		{"insecure reference", "http://example.org/pricing", "token", expression, expression, false},
		{"credential in reference", "https://user:pass@example.org/pricing", "token", expression, expression, false},
		{"metadata mismatch", "https://example.org/pricing", "token", expression, `tier("standard", p * 3)`, false},
		{"negative price", "https://example.org/pricing", "token", `tier("standard", p * -2)`, `tier("standard", p * -2)`, false},
		{"unknown unit", "https://example.org/pricing", "unknown", expression, expression, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := common.Marshal(map[string]any{"schema_version": 2, "success": true, "data": map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": tc.expression}}, "source_metadata": map[string]any{"m": map[string]string{"source": "official_manual", "source_url": tc.source, "unit": tc.unit, "expression": tc.metadataExpression}}})
			require.NoError(t, err)
			result, err := readModelPriceCatalog(strings.NewReader(string(body)))
			if tc.valid {
				require.NoError(t, err)
				assert.Equal(t, tc.expression, result["m"])
			} else {
				require.Error(t, err)
			}
		})
	}
}
