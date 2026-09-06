package plugins_test

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/relay/channel"
	taskplugin "github.com/QuantumNous/new-api/relay/channel/task/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestXaiResponsesProtocol(t *testing.T) {
	testVideoResponsesProtocol(t, videoResponsesTestCase{
		pluginKey: "xai",
		model:     "grok-imagine-video",
		requestBody: map[string]any{
			"model":   "grok-imagine-video",
			"input":   "A ginger cat walking in the rain",
			"seconds": 6,
			"size":    "1280x720",
		},
		wantAction: "text_to_video",
		wantRequest: map[string]any{
			"model":        "grok-imagine-video",
			"prompt":       "A ginger cat walking in the rain",
			"duration":     float64(6),
			"aspect_ratio": "16:9",
			"resolution":   "720p",
		},
		wantUsageKeys:  []string{"resolution", "seconds"},
		wantVendorName: "xai",
	})
}

func loadXaiPlugin(t *testing.T) *jsplugin.LoadedPlugin {
	t.Helper()
	source, err := builtinplugins.Source("xai")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "xai"})
	require.NoError(t, err)
	return plugin
}

func callXaiHook(t *testing.T, plugin *jsplugin.LoadedPlugin, hook string, args ...any) map[string]any {
	t.Helper()
	value, err := plugin.Engine.Call(t.Context(), hook, args...)
	require.NoError(t, err)
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(encoded, &decoded))
	return decoded
}

func TestXaiBuildSubmitRequest(t *testing.T) {
	plugin := loadXaiPlugin(t)
	testCases := []struct {
		name       string
		model      string
		request    map[string]any
		wantBody   map[string]any
		wantAction string
	}{
		{
			name:    "openai size maps to 16:9 720p",
			model:   "grok-imagine-video",
			request: map[string]any{"prompt": "rain on a street", "seconds": 6, "size": "1280x720"},
			wantBody: map[string]any{
				"model":        "grok-imagine-video",
				"prompt":       "rain on a street",
				"duration":     float64(6),
				"aspect_ratio": "16:9",
				"resolution":   "720p",
			},
			wantAction: "text_to_video",
		},
		{
			name:    "native grok fields pass through",
			model:   "grok-imagine-video-1.5",
			request: map[string]any{"prompt": "lantern", "duration": 10, "aspect_ratio": "9:16", "resolution": "1080p"},
			wantBody: map[string]any{
				"model":        "grok-imagine-video-1.5",
				"prompt":       "lantern",
				"duration":     float64(10),
				"aspect_ratio": "9:16",
				"resolution":   "1080p",
			},
			wantAction: "text_to_video",
		},
		{
			name:    "image url becomes first-frame image",
			model:   "grok-imagine-video-1.5",
			request: map[string]any{"prompt": "animate", "image": "https://cdn.example/cat.png", "duration": 6},
			wantBody: map[string]any{
				"model":        "grok-imagine-video-1.5",
				"prompt":       "animate",
				"duration":     float64(6),
				"aspect_ratio": "16:9",
				"resolution":   "720p",
				"image":        map[string]any{"url": "https://cdn.example/cat.png"},
			},
			wantAction: "image_to_video",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			decoded := callXaiHook(t, plugin, "buildSubmitRequest", map[string]any{
				"requestBody":   testCase.request,
				"model":         testCase.model,
				"upstreamModel": testCase.model,
				"baseUrl":       "https://grok2api.example",
				"apiKey":        "g2a_test",
			})
			assert.Equal(t, "https://grok2api.example/v1/videos/generations", decoded["url"])
			assert.Equal(t, "POST", decoded["method"])
			assert.Equal(t, testCase.wantAction, decoded["action"])
			body, ok := decoded["body"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, testCase.wantBody, body)
		})
	}
}

func TestXaiParseSubmitAndTaskResult(t *testing.T) {
	plugin := loadXaiPlugin(t)
	submitted := callXaiHook(t, plugin, "parseSubmitResponse", map[string]any{}, map[string]any{
		"statusCode": 200,
		"body":       map[string]any{"request_id": "video_abc"},
	})
	assert.Equal(t, "video_abc", submitted["taskId"])

	pending := callXaiHook(t, plugin, "parseTaskResult", map[string]any{"taskId": "video_abc"}, map[string]any{
		"status": "pending", "progress": 42, "model": "grok-imagine-video",
	})
	assert.Equal(t, "IN_PROGRESS", pending["status"])
	assert.Equal(t, "42%", pending["progress"])

	done := callXaiHook(t, plugin, "parseTaskResult", map[string]any{"taskId": "video_abc"}, map[string]any{
		"status": "done",
		"progress": 100,
		"model":  "grok-imagine-video",
		"video":  map[string]any{"url": "https://cdn.example/out.mp4", "duration": 6},
	})
	assert.Equal(t, "SUCCESS", done["status"])
	assert.Equal(t, "https://cdn.example/out.mp4", done["url"])
	assert.Equal(t, "https://cdn.example/out.mp4", done["remoteUrl"])

	failed := callXaiHook(t, plugin, "parseTaskResult", map[string]any{"taskId": "video_abc"}, map[string]any{
		"status": "failed",
		"error":  map[string]any{"code": "upstream_quota_exhausted", "message": "上游账号额度等待恢复"},
	})
	assert.Equal(t, "FAILURE", failed["status"])
	assert.Equal(t, "上游账号额度等待恢复", failed["reason"])
}

func TestXaiArtifactContentProxy(t *testing.T) {
	plugin := loadXaiPlugin(t)
	adaptor := taskplugin.New(plugin)
	adaptor.Init(&relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey:         "g2a_test",
			ChannelBaseUrl: "https://grok2api.example",
		},
	})

	poll, err := common.Marshal(map[string]any{
		"status": "done",
		"video":  map[string]any{"url": "https://cdn.example/out.mp4", "duration": 6},
	})
	require.NoError(t, err)
	task := &model.Task{TaskID: "video_abc", Status: model.TaskStatusSuccess, Data: poll}

	artifacts, err := adaptor.ListArtifacts(task)
	require.NoError(t, err)
	assert.Equal(t, []channel.TaskArtifact{{Key: "video", Type: "video"}}, artifacts)

	descriptor, err := adaptor.BuildContentRequest(task, "video", channel.TaskArtifactClientRequest{Method: http.MethodGet})
	require.NoError(t, err)
	require.NotNil(t, descriptor)
	assert.Equal(t, "https://cdn.example/out.mp4", descriptor.URL)
	assert.True(t, descriptor.Credentialless)

	empty := &model.Task{TaskID: "video_abc", Status: model.TaskStatusSuccess}
	fallback, err := adaptor.BuildContentRequest(empty, "video", channel.TaskArtifactClientRequest{Method: http.MethodGet})
	require.NoError(t, err)
	assert.Equal(t, "https://grok2api.example/v1/videos/video_abc/content", fallback.URL)
	assert.False(t, fallback.Credentialless)
	assert.Equal(t, map[string]string{"Authorization": "Bearer g2a_test"}, fallback.Headers)
}
