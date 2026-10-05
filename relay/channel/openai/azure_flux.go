package openai

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// FLUX.2 uses the Azure BFL provider API rather than an OpenAI deployment URL.
// Select by the mapped model so aliases work without affecting other Azure APIs.
func azureFlux2ModelPath(info *relaycommon.RelayInfo) string {
	if info == nil || info.ChannelMeta == nil || info.ChannelType != constant.ChannelTypeAzure || info.RelayMode != relayconstant.RelayModeImagesGenerations {
		return ""
	}
	switch strings.ToLower(info.UpstreamModelName) {
	case "flux.2-pro":
		return "flux-2-pro"
	case "flux.2-flex":
		return "flux-2-flex"
	default:
		return ""
	}
}

type azureFlux2ImageRequest struct {
	Model            string   `json:"model"`
	Prompt           string   `json:"prompt"`
	N                *uint    `json:"n,omitempty"`
	Width            *int     `json:"width,omitempty"`
	Height           *int     `json:"height,omitempty"`
	Seed             *int64   `json:"seed,omitempty"`
	Steps            *int     `json:"steps,omitempty"`
	Guidance         *float64 `json:"guidance,omitempty"`
	SafetyTolerance  *int     `json:"safety_tolerance,omitempty"`
	PromptUpsampling *bool    `json:"prompt_upsampling,omitempty"`
	OutputFormat     *string  `json:"output_format,omitempty"`
}

func convertAzureFlux2ImageRequest(info *relaycommon.RelayInfo, request dto.ImageRequest) (*azureFlux2ImageRequest, error) {
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, types.NewErrorWithStatusCode(fmt.Errorf("prompt is required"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	n := uint(1)
	if request.N != nil && *request.N > 0 {
		n = *request.N
	}
	if n > dto.MaxImageN {
		return nil, types.NewErrorWithStatusCode(fmt.Errorf("n must not exceed %d", dto.MaxImageN), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	// The image DTO deliberately omits Extra during JSON serialization. Read
	// native options here and forward only fields supported by this provider.
	extra := request.Extra
	if _, ok := extra["num_images"]; ok {
		return nil, types.NewErrorWithStatusCode(fmt.Errorf("use n for the image count, not num_images"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	extraBody, err := common.Marshal(extra)
	if err != nil {
		return nil, types.NewErrorWithStatusCode(fmt.Errorf("invalid FLUX.2 options: %w", err), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	var upstream azureFlux2ImageRequest
	if err := common.Unmarshal(extraBody, &upstream); err != nil {
		return nil, types.NewErrorWithStatusCode(fmt.Errorf("invalid FLUX.2 options: %w", err), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	upstream.Model = info.UpstreamModelName
	upstream.Prompt = request.Prompt
	upstream.N = &n
	if len(request.OutputFormat) > 0 {
		if err := common.Unmarshal(request.OutputFormat, &upstream.OutputFormat); err != nil {
			return nil, types.NewErrorWithStatusCode(fmt.Errorf("output_format must be a string: %w", err), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
	}

	width, height := 1024, 1024
	if request.Size != "" && request.Size != "auto" {
		w, h, found := strings.Cut(request.Size, "x")
		var widthErr, heightErr error
		width, widthErr = strconv.Atoi(w)
		height, heightErr = strconv.Atoi(h)
		if !found || widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
			return nil, types.NewErrorWithStatusCode(fmt.Errorf("size must be auto or positive WIDTHxHEIGHT dimensions"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		if (upstream.Width != nil && *upstream.Width != width) || (upstream.Height != nil && *upstream.Height != height) {
			return nil, types.NewErrorWithStatusCode(fmt.Errorf("size conflicts with width or height"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
	}
	if upstream.Width == nil {
		upstream.Width = &width
	}
	if upstream.Height == nil {
		upstream.Height = &height
	}
	if *upstream.Width <= 0 || *upstream.Height <= 0 {
		return nil, types.NewErrorWithStatusCode(fmt.Errorf("width and height must be positive integers"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	return &upstream, nil
}
