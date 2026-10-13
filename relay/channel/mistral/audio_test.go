package mistral

import (
	"bytes"
	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
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
