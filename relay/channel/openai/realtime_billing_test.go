package openai

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

func TestRealtimeUsagePreservesModalCacheAcrossResponses(t *testing.T) {
	usage := &dto.RealtimeUsage{}
	response := &dto.RealtimeUsage{TotalTokens: 1100, InputTokens: 1000, OutputTokens: 100, InputTokenDetails: dto.InputTokenDetails{TextTokens: 400, AudioTokens: 600, CachedTokens: 300, CachedTokensDetails: &dto.CachedTokenDetails{TextTokens: common.GetPointer(100), AudioTokens: common.GetPointer(200)}}}
	require.NoError(t, accumulateRealtimeUsage(usage, response))
	require.NoError(t, accumulateRealtimeUsage(usage, response))
	assert.Equal(t, 2200, usage.TotalTokens)
	assert.Equal(t, 600, usage.InputTokenDetails.CachedTokens)
	assert.Equal(t, 400, *usage.InputTokenDetails.CachedTokensDetails.AudioTokens)
	assert.Equal(t, 200, *response.InputTokenDetails.CachedTokensDetails.AudioTokens)
	before := *usage
	require.Error(t, accumulateRealtimeUsage(usage, &dto.RealtimeUsage{TotalTokens: math.MaxInt32}))
	assert.Equal(t, before, *usage)
	require.Error(t, accumulateRealtimeUsage(usage, &dto.RealtimeUsage{InputTokenDetails: dto.InputTokenDetails{AudioTokens: -1}}))
	assert.Equal(t, before, *usage)
}
