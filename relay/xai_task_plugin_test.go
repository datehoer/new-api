package relay

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveTaskPluginForXaiChannel(t *testing.T) {
	plugin, ok := ResolveTaskPluginForPlatform(
		pluginruntime.DefaultRegistry.Generation(),
		constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeXai)),
	)
	require.True(t, ok)
	assert.Equal(t, "xai", plugin.Meta.Key)

	adaptor := GetTaskAdaptor(constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeXai)))
	require.NotNil(t, adaptor)
}
