package common

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
)

func TestXaiVideoModelsAdvertiseOpenAIVideoEndpoint(t *testing.T) {
	got := GetEndpointTypesByChannelType(constant.ChannelTypeXai, "grok-imagine-video")
	assert.Equal(t, constant.EndpointTypeOpenAIVideo, got[0])
	assert.Contains(t, got, constant.EndpointTypeOpenAI)
	assert.Contains(t, got, constant.EndpointTypeOpenAIResponse)

	got15 := GetEndpointTypesByChannelType(constant.ChannelTypeXai, "grok-imagine-video-1.5")
	assert.Equal(t, constant.EndpointTypeOpenAIVideo, got15[0])

	chat := GetEndpointTypesByChannelType(constant.ChannelTypeXai, "grok-chat-fast")
	assert.NotContains(t, chat, constant.EndpointTypeOpenAIVideo)
}
