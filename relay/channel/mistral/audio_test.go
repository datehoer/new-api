package mistral

import (
	"bytes"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMistralFileTranscriptionPreservesMultipartAndActualDuration(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "voxtral-mini-latest"))
	require.NoError(t, writer.WriteField("language", "en"))
	file, err := writer.CreateFormFile("file", "sample.wav")
	require.NoError(t, err)
	_, err = file.Write([]byte("test audio payload"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/audio/transcriptions", &body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeAudioTranscription, BillingAudioSeconds: common.GetPointer(60.0)}
	info.SetEstimatePromptTokens(1000)
	a := &Adaptor{}
	request, err := a.ConvertAudioRequest(c, info, dto.AudioRequest{Model: "voxtral-mini-2602", ResponseFormat: "json"})
	require.NoError(t, err)
	_, params, err := mime.ParseMediaType(c.Request.Header.Get("Content-Type"))
	require.NoError(t, err)
	form, err := multipart.NewReader(request, params["boundary"]).ReadForm(1024)
	require.NoError(t, err)
	defer form.RemoveAll()
	assert.Equal(t, []string{"voxtral-mini-2602"}, form.Value["model"])
	assert.Equal(t, []string{"en"}, form.Value["language"])
	f, err := form.File["file"][0].Open()
	require.NoError(t, err)
	payload, err := io.ReadAll(f)
	require.NoError(t, err)
	f.Close()
	assert.Equal(t, "test audio payload", string(payload))
	response := httptest.NewRecorder()
	response.Header().Set("Content-Type", "application/json")
	response.WriteString(`{"text":"hello","usage":{"prompt_audio_seconds":70.25,"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}}`)
	usage, apiErr := a.DoResponse(c, response.Result(), info)
	require.Nil(t, apiErr)
	assert.Equal(t, 70.25, *info.BillingAudioSeconds)
	assert.Equal(t, 5, usage.(*dto.Usage).TotalTokens)
	_, err = a.ConvertAudioRequest(c, &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeAudioSpeech}, dto.AudioRequest{Model: "voxtral-mini-tts-latest", Input: strings.Repeat("a", 3)})
	require.Error(t, err)
}

func TestOCRPageSelectionReservationAndActualUsage(t *testing.T) {
	saved := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	expression := `param("document_annotation_format") != nil || param("bbox_annotation_format") != nil ? tier("annotated", usd(ocr_pages * 0.003)) : tier("standard", usd(ocr_pages * 0.002))`
	encoded, _ := common.Marshal(map[string]string{"mistral-ocr-2512": expression})
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": `{"mistral-ocr-2512":"tiered_expr"}`, "billing_setting.billing_expr": string(encoded)}))
	for _, tc := range []struct {
		pages string
		want  int
		valid bool
	}{{`[0,2,2]`, 2, true}, {`"0,2-4"`, 4, true}, {`[1.1]`, 0, false}, {`"0-1000000000"`, 0, false}, {`[]`, 0, false}, {`[-1]`, 0, false}} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/ocr", strings.NewReader(`{"model":"mistral-ocr-2512","document":{"type":"document_url","document_url":"https://example.org/test.pdf"},"pages":`+tc.pages+`}`))
		c.Request.Header.Set("Content-Type", "application/json")
		request, err := helper.GetAndValidateOCRRequest(c)
		if !tc.valid {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		assert.Equal(t, tc.want, request.PageReservation)
		var pages []int
		require.NoError(t, common.Unmarshal(request.Body["pages"], &pages))
		assert.Len(t, pages, tc.want)
		info := &relaycommon.RelayInfo{OriginModelName: "mistral-ocr-2512", Request: request, RelayMode: relayconstant.RelayModeOCR, UsingGroup: "default", UserGroup: "default", PriceData: hosttypes.PriceData{GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}}}
		_, err = helper.ModelPriceHelper(c, info, 0, &relaytypes.TokenCountMeta{})
		require.NoError(t, err)
		assert.True(t, info.ForcePreConsume)
		assert.Equal(t, tc.want*1000, info.TieredBillingSnapshot.EstimatedQuotaAfterGroup)
		response := httptest.NewRecorder()
		response.Header().Set("Content-Type", "application/json")
		response.WriteString(`{"pages":[{"index":0,"markdown":"hello"}],"usage_info":{"pages_processed":1}}`)
		usage, apiErr := (&Adaptor{}).DoResponse(c, response.Result(), info)
		require.Nil(t, apiErr)
		assert.Zero(t, usage.(*dto.Usage).TotalTokens)
		assert.Equal(t, 1.0, info.BillingOCRPages)
		ok, quota, result := service.TryTieredSettle(info, billingexpr.TokenParams{})
		require.True(t, ok)
		assert.Equal(t, 1000, quota)
		require.NotNil(t, result)
		assert.Equal(t, 1.0, result.BillingTokens.OCRPages)
	}
}

func TestOCRBoundsAndUntrustedUsage(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"model":"mistral-ocr-2512","document":{"type":"document_url","document_url":"https://example.org/doc.pdf"}}`, false},
		{`{"model":"mistral-ocr-2512","document":{"type":"image_url","image_url":"https://example.org/page.png"}}`, true},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/ocr", strings.NewReader(tc.body))
		c.Request.Header.Set("Content-Type", "application/json")
		request, err := helper.GetAndValidateOCRRequest(c)
		if !tc.valid {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		assert.Equal(t, 1, request.PageReservation)
		for _, responseBody := range []string{`{"usage_info":{"pages_processed":2}}`, `{"usage_info":{"pages_processed":0}}`, `{"pages":[]}`} {
			info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeOCR, BillingOCRPages: 1}
			response := httptest.NewRecorder()
			response.WriteString(responseBody)
			_, apiErr := (&Adaptor{}).DoResponse(c, response.Result(), info)
			require.Nil(t, apiErr)
			assert.True(t, math.IsNaN(info.BillingOCRPages), "invalid upstream measurements must retain the reservation")
		}
	}
}

func TestOCRAnnotatedPagePriceIsFrozenFromNativeBody(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/ocr", strings.NewReader(`{"model":"mistral-ocr-2512","document":{"type":"image_url","image_url":"https://example.org/page.png"},"document_annotation_format":{"type":"json_object"},"pages":[0]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	request, err := helper.GetAndValidateOCRRequest(c)
	require.NoError(t, err)
	frozen, err := helper.ResolveIncomingBillingExprRequestInput(c, &relaycommon.RelayInfo{})
	require.NoError(t, err)
	expression := `param("document_annotation_format") != nil ? tier("annotated", usd(ocr_pages * 0.003)) : tier("standard", usd(ocr_pages * 0.002))`
	cost, trace, err := billingexpr.RunExprWithRequest(expression, billingexpr.TokenParams{OCRPages: float64(request.PageReservation)}, frozen)
	require.NoError(t, err)
	assert.Equal(t, "annotated", trace.MatchedTier)
	assert.InDelta(t, 3000, cost, 1e-8)
}
