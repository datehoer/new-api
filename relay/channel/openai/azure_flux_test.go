package openai

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAzureFlux2ImageRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name      string
		model     string
		modelPath string
		body      string
		wantBody  string
		override  map[string]interface{}
		wantAuth  string
	}{
		{
			name:      "pro with OpenAI size and native zero values",
			model:     "FLUX.2-pro",
			modelPath: "flux-2-pro",
			body:      `{"model":"flux-alias","prompt":"A red fox","n":3,"size":"1536x1024","quality":"high","response_format":"b64_json","seed":0,"safety_tolerance":0,"prompt_upsampling":false,"output_format":"png","unknown_option":7}`,
			wantBody:  `{"model":"FLUX.2-pro","prompt":"A red fox","n":3,"width":1536,"height":1024,"seed":0,"safety_tolerance":0,"prompt_upsampling":false,"output_format":"png"}`,
			wantAuth:  "Bearer azure-test-key",
		},
		{
			name:      "flex preserves native dimensions and options after copying",
			model:     "FLUX.2-flex",
			modelPath: "flux-2-flex",
			body:      `{"model":"flux-alias","prompt":"A red fox","size":"auto","width":768,"height":1024,"steps":10,"guidance":1.5}`,
			wantBody:  `{"model":"FLUX.2-flex","prompt":"A red fox","n":1,"width":768,"height":1024,"steps":10,"guidance":1.5}`,
			wantAuth:  "Bearer azure-test-key",
		},
		{
			name:      "default dimensions and header override",
			model:     "FLUX.2-pro",
			modelPath: "flux-2-pro",
			body:      `{"model":"flux-alias","prompt":"A red fox","n":0}`,
			wantBody:  `{"model":"FLUX.2-pro","prompt":"A red fox","n":1,"width":1024,"height":1024}`,
			override:  map[string]interface{}{"Authorization": "Bearer overridden-test-key"},
			wantAuth:  "Bearer overridden-test-key",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type capturedRequest struct {
				method, path, query, auth, apiKey, contentType string
				body                                           []byte
				err                                            error
			}
			captured := make(chan capturedRequest, 1)
			responseBody := `{"created":1710000000,"data":[{"b64_json":"aW1hZ2U="}],"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				captured <- capturedRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("api-key"), r.Header.Get("Content-Type"), body, err}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, responseBody)
			}))
			t.Cleanup(server.Close)

			var original dto.ImageRequest
			require.NoError(t, common.Unmarshal([]byte(tc.body), &original))
			request, err := common.DeepCopy(&original)
			require.NoError(t, err)
			assert.Equal(t, original.Extra, request.Extra)

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("Authorization", "Bearer client-test-key")
			c.Set("model_mapping", `{"flux-alias":"`+tc.model+`"}`)
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType:       constant.ChannelTypeAzure,
					ChannelBaseUrl:    server.URL + "/",
					ApiKey:            "azure-test-key",
					ApiVersion:        "2025-01-01-preview",
					UpstreamModelName: original.Model,
					HeadersOverride:   tc.override,
				},
				RelayMode:       relayconstant.RelayModeImagesGenerations,
				OriginModelName: original.Model,
				RequestURLPath:  "/v1/images/generations?api-version=client-version",
				Request:         &original,
			}
			require.NoError(t, helper.ModelMappedHelper(c, info, request))
			info.PriceData.UsePrice = true
			info.PriceData.AddOtherRatio("n", 3)
			adaptor := &Adaptor{}
			adaptor.Init(info)
			converted, err := adaptor.ConvertImageRequest(c, info, *request)
			require.NoError(t, err)
			body, err := common.Marshal(converted)
			require.NoError(t, err)
			rawResponse, err := adaptor.DoRequest(c, info, bytes.NewReader(body))
			require.NoError(t, err)
			response, ok := rawResponse.(*http.Response)
			require.True(t, ok)
			require.Equal(t, http.StatusOK, response.StatusCode)
			usage, apiErr := adaptor.DoResponse(c, response, info)
			require.Nil(t, apiErr)
			require.NotNil(t, usage)

			got := <-captured
			require.NoError(t, got.err)
			assert.Equal(t, http.MethodPost, got.method)
			assert.Equal(t, "/providers/blackforestlabs/v1/"+tc.modelPath, got.path)
			assert.Equal(t, "api-version=preview", got.query)
			assert.Equal(t, tc.wantAuth, got.auth)
			assert.Empty(t, got.apiKey)
			assert.Equal(t, "application/json", got.contentType)
			assert.JSONEq(t, tc.wantBody, string(got.body))
			assert.Equal(t, responseBody, recorder.Body.String(), "base64 image data must reach the client unchanged")
			assert.Equal(t, 8, usage.(*dto.Usage).TotalTokens)
			assert.Equal(t, 1.0, info.PriceData.OtherRatios()["n"], "settle against the returned image count")
		})
	}
}

func TestAzureFlux2InvalidImageParameters(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty prompt", `{"model":"FLUX.2-pro","prompt":" "}`},
		{"malformed size", `{"model":"FLUX.2-pro","prompt":"fox","size":"1024"}`},
		{"negative size", `{"model":"FLUX.2-pro","prompt":"fox","size":"-1x1024"}`},
		{"zero native width", `{"model":"FLUX.2-pro","prompt":"fox","width":0}`},
		{"fractional native width", `{"model":"FLUX.2-pro","prompt":"fox","width":1024.5}`},
		{"conflicting dimensions", `{"model":"FLUX.2-pro","prompt":"fox","size":"1024x1024","width":768}`},
		{"invalid output format type", `{"model":"FLUX.2-pro","prompt":"fox","output_format":42}`},
		{"count above billing bound", `{"model":"FLUX.2-pro","prompt":"fox","n":129}`},
		{"native count alias bypass", `{"model":"FLUX.2-pro","prompt":"fox","num_images":129}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var original dto.ImageRequest
			require.NoError(t, common.Unmarshal([]byte(tc.body), &original))
			request, err := common.DeepCopy(&original)
			require.NoError(t, err)
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeAzure, UpstreamModelName: original.Model},
				RelayMode:   relayconstant.RelayModeImagesGenerations,
				Request:     &original,
			}
			_, err = (&Adaptor{}).ConvertImageRequest(nil, info, *request)
			require.Error(t, err)
			// ImageHelper wraps conversion errors; invalid requests must remain a
			// non-retryable 400 and never trigger a second paid generation.
			apiErr := types.NewError(err, types.ErrorCodeConvertRequestFailed)
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			assert.True(t, types.IsSkipRetryError(apiErr))
		})
	}

	maxN := uint(dto.MaxImageN)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "FLUX.2-pro"}}
	_, err := convertAzureFlux2ImageRequest(info, dto.ImageRequest{Prompt: "fox", N: &maxN})
	require.NoError(t, err, "the provider adapter must use the shared image count limit")
}

func TestAzureFlux2RequestURLs(t *testing.T) {
	for _, tc := range []struct{ name, baseURL, model, wantURL string }{
		{"Foundry pro", "https://resource.services.ai.azure.com", "FLUX.2-pro", "https://resource.services.ai.azure.com/providers/blackforestlabs/v1/flux-2-pro?api-version=preview"},
		{"Foundry flex trailing slash", "https://resource.services.ai.azure.com/", "FLUX.2-flex", "https://resource.services.ai.azure.com/providers/blackforestlabs/v1/flux-2-flex?api-version=preview"},
		{"Azure OpenAI hostname stays configured", "https://resource.openai.azure.com", "FLUX.2-pro", "https://resource.openai.azure.com/providers/blackforestlabs/v1/flux-2-pro?api-version=preview"},
		{"Azure hostname and explicit port stay configured", "https://resource.openai.azure.com:443", "FLUX.2-flex", "https://resource.openai.azure.com:443/providers/blackforestlabs/v1/flux-2-flex?api-version=preview"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType:       constant.ChannelTypeAzure,
					ChannelBaseUrl:    tc.baseURL,
					UpstreamModelName: tc.model,
					ApiVersion:        "2025-01-01-preview",
				},
				RelayMode: relayconstant.RelayModeImagesGenerations,
			}
			url, err := (&Adaptor{}).GetRequestURL(info)
			require.NoError(t, err)
			assert.Equal(t, tc.wantURL, url)
		})
	}
}

func TestAzureFlux2LeavesOtherRoutesUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name, model, path, auth, apiKey string
		channelType, mode               int
	}{
		{"Azure GPT image", "gpt-image-1", "/openai/deployments/gpt-image-1/images/generations?api-version=test-version", "", "azure-test-key", constant.ChannelTypeAzure, relayconstant.RelayModeImagesGenerations},
		{"Azure FLUX editing", "FLUX.2-pro", "/openai/deployments/FLUX.2-pro/images/edits?api-version=test-version", "", "azure-test-key", constant.ChannelTypeAzure, relayconstant.RelayModeImagesEdits},
		{"OpenAI FLUX generation", "FLUX.2-pro", "/v1/images/generations", "Bearer azure-test-key", "", constant.ChannelTypeOpenAI, relayconstant.RelayModeImagesGenerations},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestPath := "/v1/images/generations"
			if tc.mode == relayconstant.RelayModeImagesEdits {
				requestPath = "/v1/images/edits"
			}
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType:       tc.channelType,
					ChannelBaseUrl:    "https://azure.example",
					ApiVersion:        "test-version",
					ApiKey:            "azure-test-key",
					ChannelCreateTime: constant.AzureNoRemoveDotTime,
					UpstreamModelName: tc.model,
				},
				RelayMode:      tc.mode,
				RequestURLPath: requestPath,
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, requestPath, nil)
			c.Request.Header.Set("Content-Type", "application/json")
			adaptor := &Adaptor{}
			url, err := adaptor.GetRequestURL(info)
			require.NoError(t, err)
			assert.Equal(t, "https://azure.example"+tc.path, url)
			header := make(http.Header)
			require.NoError(t, adaptor.SetupRequestHeader(c, &header, info))
			assert.Equal(t, tc.auth, header.Get("Authorization"))
			assert.Equal(t, tc.apiKey, header.Get("api-key"))
			request := dto.ImageRequest{Model: tc.model, Prompt: "fox", Size: "1024x1024", Quality: "high"}
			converted, err := adaptor.ConvertImageRequest(c, info, request)
			require.NoError(t, err)
			assert.Equal(t, request, converted)
		})
	}
}
